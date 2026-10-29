// gocryptfs-lib is the C-callable shim the macOS app links. It is built with
// -buildmode=c-shared into libgocryptfs.dylib and exposes the engine in
// github.com/adrianm/imagethat/core/vault through the C API declared in
// CLibGocryptfs/libgocryptfs.h.
//
// The shim owns no crypto and no I/O logic of its own: every export maps onto
// a vault.Session call. It keeps the session table, converts between C and Go
// memory, caches open file handles for range reads, and runs streaming writers.
//
// Plaintext never touches disk here: decrypted bytes are copied into C memory
// the caller frees (GocryptfsFreeDecryptResult zeroes them first), and the Go
// copies are wiped as soon as they have been handed over. Nothing is logged.
package main

/*
#include "shim.h"
*/
import "C"

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sync"
	"unsafe"

	"github.com/adrianm/imagethat/core/cache"
	"github.com/adrianm/imagethat/core/remote"
	"github.com/adrianm/imagethat/core/thumb"
	"github.com/adrianm/imagethat/core/vault"
)

// Return codes shared by the open and write exports. The app's
// GocryptfsBridge.swift switches on these; they are part of the C API.
const (
	rcOK            = 0
	rcNoConfig      = -1 // also: invalid session, read-only backend, invalid writer
	rcWrongPassword = -2 // also: source path not found
	rcCrypto        = -3 // also: destination directory unreadable
	rcExists        = -4
	rcBackend       = -5
)

// openFile is a cached decrypted view of one file, shared between range reads
// so a video scrub does not reopen and re-parse the header per 256 KiB chunk.
type openFile struct {
	f    *vault.File
	refs int32 // guarded by session.mu; closed when it reaches 0
}

type session struct {
	s *vault.Session

	mu    sync.Mutex
	files map[string]*openFile // plaintext path -> handle, at most maxOpenFiles
	order []string             // oldest first, for eviction

	// thumbs is the preview service, created by GocryptfsThumbConfigure or
	// lazily (memory only) on first use.
	thumbs *thumb.Service
}

const maxOpenFiles = 16

var (
	sessions  = make(map[int]*session)
	sessionMu sync.RWMutex
	nextID    = 1
)

func getSession(id C.int) *session {
	sessionMu.RLock()
	defer sessionMu.RUnlock()
	return sessions[int(id)]
}

func register(s *vault.Session) C.int {
	sessionMu.Lock()
	defer sessionMu.Unlock()
	id := nextID
	nextID++
	sessions[id] = &session{s: s, files: make(map[string]*openFile)}
	return C.int(id)
}

// openCode maps an unlock failure onto the C API's return codes.
func openCode(err error) C.int {
	switch {
	case errors.Is(err, vault.ErrWrongPassword):
		return rcWrongPassword
	case errors.Is(err, vault.ErrNoConfig), errors.Is(err, vault.ErrPermission):
		return rcNoConfig
	default:
		return rcCrypto
	}
}

// --- VFS over host callbacks -------------------------------------------------

// callbackFS adapts the three host function pointers to vault.VFS. The host
// hands over gocryptfs.conf separately (it has already downloaded it), so
// ReadFile serves that one name from memory.
type callbackFS struct {
	readAtFn   C.VFSReadAtFn
	fileSizeFn C.VFSFileSizeFn
	listDirFn  C.VFSListDirFn
	ctx        unsafe.Pointer
	config     []byte
}

var _ vault.VFS = (*callbackFS)(nil)

func (v *callbackFS) ReadAt(p string, offset int64, buf []byte) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	cPath := C.CString(p)
	defer C.free(unsafe.Pointer(cPath))
	n := C.call_VFSReadAtFn(v.readAtFn, v.ctx, cPath,
		C.int64_t(offset), C.int64_t(len(buf)),
		(*C.uint8_t)(unsafe.Pointer(&buf[0])), C.int64_t(len(buf)))
	if n < 0 {
		return 0, fmt.Errorf("vfs ReadAt %s offset=%d: callback failed", p, offset)
	}
	if int(n) < len(buf) {
		return int(n), io.EOF
	}
	return int(n), nil
}

func (v *callbackFS) FileSize(p string) (int64, error) {
	cPath := C.CString(p)
	defer C.free(unsafe.Pointer(cPath))
	sz := C.call_VFSFileSizeFn(v.fileSizeFn, v.ctx, cPath)
	if sz < 0 {
		return 0, &fs.PathError{Op: "stat", Path: p, Err: fs.ErrNotExist}
	}
	return int64(sz), nil
}

func (v *callbackFS) ReadDir(p string) ([]vault.DirEntry, error) {
	cPath := C.CString(p)
	defer C.free(unsafe.Pointer(cPath))
	lr := C.call_VFSListDirFn(v.listDirFn, v.ctx, cPath)
	defer GocryptfsFreeListResult(lr)
	if lr.error != nil {
		return nil, fmt.Errorf("vfs ReadDir %s: %s", p, C.GoString(lr.error))
	}
	count := int(lr.count)
	if count == 0 {
		return nil, nil
	}
	slice := unsafe.Slice(lr.entries, count)
	out := make([]vault.DirEntry, count)
	for i, e := range slice {
		out[i] = vault.DirEntry{Name: C.GoString(e.name), IsDir: e.is_dir != 0, Size: int64(e.size)}
	}
	return out, nil
}

func (v *callbackFS) ReadFile(p string) ([]byte, error) {
	if p == "gocryptfs.conf" && v.config != nil {
		return append([]byte(nil), v.config...), nil
	}
	sz, err := v.FileSize(p)
	if err != nil {
		return nil, err
	}
	if sz == 0 {
		return []byte{}, nil
	}
	buf := make([]byte, sz)
	n, err := v.ReadAt(p, 0, buf)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf[:n], nil
}

// --- Open / close ------------------------------------------------------------

//export GocryptfsOpen
func GocryptfsOpen(vaultPath *C.char, password *C.char) C.int {
	s, err := vault.OpenLocal(C.GoString(vaultPath), C.GoString(password))
	if err != nil {
		return openCode(err)
	}
	return register(s)
}

//export GocryptfsOpenVFS
func GocryptfsOpenVFS(
	configData *C.char, configDataLen C.int,
	password *C.char,
	readAtFn C.VFSReadAtFn, fileSizeFn C.VFSFileSizeFn, listDirFn C.VFSListDirFn,
	ctx unsafe.Pointer,
) C.int {
	v := &callbackFS{
		readAtFn:   readAtFn,
		fileSizeFn: fileSizeFn,
		listDirFn:  listDirFn,
		ctx:        ctx,
		config:     C.GoBytes(unsafe.Pointer(configData), configDataLen),
	}
	s, err := vault.Open(v, C.GoString(password))
	if err != nil {
		return openCode(err)
	}
	return register(s)
}

// GocryptfsOpenRemote opens a vault on an imagethat-web server in ciphertext
// mode: the server sends the vault's encrypted files and this library
// decrypts them, so nothing the server sends is plaintext. pinnedSHA256 is
// the server certificate's SHA-256 in hex for a self-signed server, or empty.
// The session is read-only (writes go through the server's decrypted API)
// and works with every read and thumbnail export.
//
// Returns a session id, or -1 no such vault, -2 wrong password, -3 network or
// server error, -6 certificate does not match the pin.
//
//export GocryptfsOpenRemote
func GocryptfsOpenRemote(baseURL *C.char, vaultID *C.char, password *C.char, pinnedSHA256 *C.char) C.int {
	client, err := remote.New(C.GoString(baseURL), remote.Options{PinnedSHA256: C.GoString(pinnedSHA256)})
	if err != nil {
		return rcCrypto
	}
	s, err := client.OpenVault(context.Background(), C.GoString(vaultID), C.GoString(password))
	switch {
	case err == nil:
		return register(s)
	case errors.Is(err, remote.ErrWrongPassword), errors.Is(err, vault.ErrWrongPassword):
		return rcWrongPassword
	case errors.Is(err, remote.ErrNotFound), errors.Is(err, vault.ErrNoConfig):
		return rcNoConfig
	case errors.Is(err, remote.ErrCertMismatch):
		return -6
	default:
		return rcCrypto
	}
}

//export GocryptfsClose
func GocryptfsClose(sessionID C.int) {
	sessionMu.Lock()
	sess, ok := sessions[int(sessionID)]
	delete(sessions, int(sessionID))
	sessionMu.Unlock()
	if !ok {
		return
	}
	sess.dropAllFiles()
	sess.mu.Lock()
	if sess.thumbs != nil {
		sess.thumbs.Close()
		sess.thumbs = nil
	}
	sess.mu.Unlock()
	_ = sess.s.Close()
}

// --- Open-file cache -----------------------------------------------------------

// acquire returns a decrypted handle for a plaintext path, opening and caching
// it on first use. The caller must call release when done.
func (sess *session) acquire(plainPath string) (*openFile, error) {
	sess.mu.Lock()
	if of, ok := sess.files[plainPath]; ok {
		of.refs++
		sess.mu.Unlock()
		return of, nil
	}
	sess.mu.Unlock()

	f, err := sess.s.Open(plainPath)
	if err != nil {
		return nil, err
	}
	of := &openFile{f: f, refs: 2} // one for the cache, one for the caller

	sess.mu.Lock()
	defer sess.mu.Unlock()
	if existing, ok := sess.files[plainPath]; ok {
		// Lost a race with another opener; use theirs.
		f.Close()
		existing.refs++
		return existing, nil
	}
	sess.files[plainPath] = of
	sess.order = append(sess.order, plainPath)
	for len(sess.files) > maxOpenFiles {
		victim := sess.order[0]
		sess.order = sess.order[1:]
		if v, ok := sess.files[victim]; ok {
			delete(sess.files, victim)
			sess.releaseLocked(v)
		}
	}
	return of, nil
}

func (sess *session) release(of *openFile) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.releaseLocked(of)
}

func (sess *session) releaseLocked(of *openFile) {
	of.refs--
	if of.refs == 0 {
		_ = of.f.Close()
	}
}

// dropFile forgets a cached handle after the file it points at changed.
func (sess *session) dropFile(plainPath string) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if of, ok := sess.files[plainPath]; ok {
		delete(sess.files, plainPath)
		for i, p := range sess.order {
			if p == plainPath {
				sess.order = append(sess.order[:i], sess.order[i+1:]...)
				break
			}
		}
		sess.releaseLocked(of)
	}
}

// dropAllFiles forgets every cached handle; used on close and after an
// operation that can move whole directories.
func (sess *session) dropAllFiles() {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	for p, of := range sess.files {
		delete(sess.files, p)
		sess.releaseLocked(of)
	}
	sess.order = nil
}

// --- Reading -----------------------------------------------------------------

//export GocryptfsListDir
func GocryptfsListDir(sessionID C.int, relativePath *C.char) C.ListResult {
	sess := getSession(sessionID)
	if sess == nil {
		return C.ListResult{error: C.CString("invalid session")}
	}
	entries, err := sess.s.ListDir(C.GoString(relativePath))
	if err != nil {
		return C.ListResult{error: C.CString(err.Error())}
	}
	if len(entries) == 0 {
		return C.ListResult{count: 0}
	}
	cEntries := (*C.DirEntry)(C.malloc(C.size_t(len(entries)) * C.size_t(unsafe.Sizeof(C.DirEntry{}))))
	slice := unsafe.Slice(cEntries, len(entries))
	for i, e := range entries {
		slice[i] = C.DirEntry{name: C.CString(e.Name), is_dir: boolToInt(e.IsDir), size: C.int64_t(e.Size)}
	}
	return C.ListResult{entries: cEntries, count: C.int(len(entries))}
}

//export GocryptfsDecryptFile
func GocryptfsDecryptFile(sessionID C.int, relativePath *C.char) C.DecryptResult {
	sess := getSession(sessionID)
	if sess == nil {
		return C.DecryptResult{error: C.CString("invalid session")}
	}
	plain, err := sess.s.ReadFile(C.GoString(relativePath))
	if err != nil {
		return C.DecryptResult{error: C.CString(err.Error())}
	}
	return toCResult(plain)
}

//export GocryptfsReadRange
func GocryptfsReadRange(sessionID C.int, relativePath *C.char, offset C.int64_t, length C.int32_t, outBuf unsafe.Pointer) C.int32_t {
	sess := getSession(sessionID)
	if sess == nil || length < 0 || offset < 0 {
		return -1
	}
	if length == 0 {
		return 0
	}
	of, err := sess.acquire(C.GoString(relativePath))
	if err != nil {
		return -1
	}
	defer sess.release(of)

	// Decrypt straight into the caller's buffer: no Go-side plaintext copy to
	// wipe afterwards.
	buf := unsafe.Slice((*byte)(outBuf), int(length))
	n, err := of.f.ReadAt(buf, int64(offset))
	if err != nil && !errors.Is(err, io.EOF) {
		return -1
	}
	return C.int32_t(n)
}

//export GocryptfsFileSize
func GocryptfsFileSize(sessionID C.int, relativePath *C.char) C.int64_t {
	sess := getSession(sessionID)
	if sess == nil {
		return -1
	}
	size, err := sess.s.FileSize(C.GoString(relativePath))
	if err != nil {
		return -1
	}
	return C.int64_t(size)
}

//export GocryptfsEncryptFileName
func GocryptfsEncryptFileName(sessionID C.int, cipherDirPath *C.char, plainName *C.char) *C.char {
	sess := getSession(sessionID)
	if sess == nil {
		return nil
	}
	name, err := sess.s.EncryptName(C.GoString(cipherDirPath), C.GoString(plainName))
	if err != nil {
		return nil
	}
	return C.CString(name)
}

// GocryptfsLongNameSidecar returns the content of the "<hashed name>.name"
// sidecar a backend must write next to an entry whose encrypted name is too
// long (GocryptfsEncryptFileName then returns gocryptfs.longname.<hash>). It
// returns an empty string when no sidecar is needed and NULL on error. Caller
// frees.
//
//export GocryptfsLongNameSidecar
func GocryptfsLongNameSidecar(sessionID C.int, cipherDirPath *C.char, plainName *C.char) *C.char {
	sess := getSession(sessionID)
	if sess == nil {
		return nil
	}
	side, err := sess.s.LongNameSidecar(C.GoString(cipherDirPath), C.GoString(plainName))
	if err != nil {
		return nil
	}
	return C.CString(side)
}

//export GocryptfsResolveCipherPath
func GocryptfsResolveCipherPath(sessionID C.int, plainRelPath *C.char, isDir C.int) *C.char {
	sess := getSession(sessionID)
	if sess == nil {
		return nil
	}
	// Files and directories resolve the same way: a long name is hashed to its
	// on-disk form in both cases, so isDir is accepted for API compatibility
	// and not needed.
	_ = isDir
	cp, err := sess.s.CipherPath(C.GoString(plainRelPath))
	if err != nil {
		return nil
	}
	return C.CString(cp)
}

// --- Whole-buffer sealing (persisted thumbnail cache) -----------------------------

//export GocryptfsEncryptData
func GocryptfsEncryptData(sessionID C.int, plaintext unsafe.Pointer, plaintextLen C.int) C.DecryptResult {
	sess := getSession(sessionID)
	if sess == nil {
		return C.DecryptResult{error: C.CString("invalid session")}
	}
	sealed, err := sess.s.SealBlob(C.GoBytes(plaintext, plaintextLen))
	if err != nil {
		return C.DecryptResult{error: C.CString(err.Error())}
	}
	return toCResult(sealed)
}

//export GocryptfsDecryptData
func GocryptfsDecryptData(sessionID C.int, ciphertext unsafe.Pointer, ciphertextLen C.int) C.DecryptResult {
	sess := getSession(sessionID)
	if sess == nil {
		return C.DecryptResult{error: C.CString("invalid session")}
	}
	plain, err := sess.s.OpenBlob(C.GoBytes(ciphertext, ciphertextLen))
	if err != nil {
		return C.DecryptResult{error: C.CString(err.Error())}
	}
	return toCResult(plain)
}

// --- Writing -------------------------------------------------------------------

// writeCode maps a mutation failure onto the C API's return codes.
func writeCode(err error) C.int {
	switch {
	case err == nil:
		return rcOK
	case errors.Is(err, vault.ErrReadOnlyBackend), errors.Is(err, vault.ErrClosed):
		return rcNoConfig
	case errors.Is(err, vault.ErrNotFound):
		return rcWrongPassword
	case errors.Is(err, vault.ErrInvalidPath):
		return rcCrypto
	case errors.Is(err, vault.ErrExists):
		return rcExists
	default:
		return rcBackend
	}
}

//export GocryptfsRenameFile
func GocryptfsRenameFile(sessionID C.int, sourcePath *C.char, destDirPath *C.char) C.int {
	sess := getSession(sessionID)
	if sess == nil {
		return rcNoConfig
	}
	src := C.GoString(sourcePath)
	dst := path.Join(C.GoString(destDirPath), path.Base(src))
	err := sess.s.Move(src, dst)
	if err == nil {
		sess.dropAllFiles()
		sess.invalidateThumb(src)
	}
	return writeCode(err)
}

//export GocryptfsMkdir
func GocryptfsMkdir(sessionID C.int, dirPath *C.char) C.int {
	sess := getSession(sessionID)
	if sess == nil {
		return rcNoConfig
	}
	return writeCode(sess.s.Mkdir(C.GoString(dirPath)))
}

//export GocryptfsDeleteFile
func GocryptfsDeleteFile(sessionID C.int, filePath *C.char) C.int {
	sess := getSession(sessionID)
	if sess == nil {
		return rcNoConfig
	}
	p := C.GoString(filePath)
	sess.dropFile(p)
	sess.invalidateThumb(p)
	return writeCode(sess.s.Remove(p))
}

//export GocryptfsDeleteDir
func GocryptfsDeleteDir(sessionID C.int, dirPath *C.char) C.int {
	sess := getSession(sessionID)
	if sess == nil {
		return rcNoConfig
	}
	sess.dropAllFiles()
	return writeCode(sess.s.Remove(C.GoString(dirPath)))
}

// GocryptfsWriteFile replaces (or creates) a file from an in-memory buffer.
// It is for small writes such as an edited image; large imports should stream
// through GocryptfsCreateFile.
//
//export GocryptfsWriteFile
func GocryptfsWriteFile(sessionID C.int, relativePath *C.char, data unsafe.Pointer, dataLen C.int) C.int {
	sess := getSession(sessionID)
	if sess == nil {
		return rcNoConfig
	}
	p := C.GoString(relativePath)
	plain := C.GoBytes(data, dataLen)
	defer wipe(plain)
	sess.dropFile(p)
	sess.invalidateThumb(p)
	_, err := sess.s.Replace(p, bytes.NewReader(plain))
	return writeCode(err)
}

// --- Streaming writers -----------------------------------------------------------
//
// A writer streams plaintext chunks into a new vault file with bounded memory:
// Session.Create runs in a goroutine fed by a pipe, encrypting block by block
// into a temp ciphertext file that is renamed into place on Close. Abort (or a
// failed Create) leaves no entry and no temp file behind.

type writer struct {
	sess *session
	path string
	pw   *io.PipeWriter
	done chan struct{}
	once sync.Once
	err  error // result of Create, valid once done is closed
}

var (
	writers      = make(map[int]*writer)
	writersMu    sync.Mutex
	nextWriterID = 1
)

var errAborted = errors.New("write aborted")

//export GocryptfsCreateFile
func GocryptfsCreateFile(sessionID C.int, relativePath *C.char) C.int {
	sess := getSession(sessionID)
	if sess == nil {
		return rcNoConfig
	}
	pr, pw := io.Pipe()
	w := &writer{sess: sess, path: C.GoString(relativePath), pw: pw, done: make(chan struct{})}
	go func() {
		_, err := sess.s.Create(w.path, pr)
		w.err = err
		if err == nil {
			err = io.EOF
		}
		// Unblock a Write still pushing data if Create bailed out early.
		pr.CloseWithError(err)
		close(w.done)
	}()
	writersMu.Lock()
	id := nextWriterID
	nextWriterID++
	writers[id] = w
	writersMu.Unlock()
	return C.int(id)
}

func takeWriter(id C.int, remove bool) *writer {
	writersMu.Lock()
	defer writersMu.Unlock()
	w := writers[int(id)]
	if remove {
		delete(writers, int(id))
	}
	return w
}

//export GocryptfsWriterWrite
func GocryptfsWriterWrite(writerID C.int, data unsafe.Pointer, dataLen C.int) C.int {
	w := takeWriter(writerID, false)
	if w == nil {
		return rcNoConfig
	}
	if dataLen <= 0 {
		return rcOK
	}
	// The pipe copies into Create's block buffer before returning, so the C
	// memory can be read in place without a Go-side copy.
	chunk := unsafe.Slice((*byte)(data), int(dataLen))
	if _, err := w.pw.Write(chunk); err != nil {
		select {
		case <-w.done:
			return writeCode(w.err)
		default:
		}
		return rcBackend
	}
	return rcOK
}

//export GocryptfsWriterClose
func GocryptfsWriterClose(writerID C.int) C.int {
	w := takeWriter(writerID, true)
	if w == nil {
		return rcNoConfig
	}
	w.once.Do(func() { w.pw.Close() })
	<-w.done
	if w.err == nil {
		w.sess.dropFile(w.path)
		w.sess.invalidateThumb(w.path)
	}
	return writeCode(w.err)
}

//export GocryptfsWriterAbort
func GocryptfsWriterAbort(writerID C.int) {
	w := takeWriter(writerID, true)
	if w == nil {
		return
	}
	w.once.Do(func() { w.pw.CloseWithError(errAborted) })
	<-w.done
}

// --- Thumbnails ----------------------------------------------------------------------
//
// Previews come from core/thumb: an encrypted memory tier, an optional disk
// tier sealed with the vault's own key, and Go's image decoders. Formats Go
// cannot decode (HEIC, RAW, video) return an empty result; the host renders
// those itself and hands the JPEG back with GocryptfsThumbPut so both tiers
// learn it. The host never implements a callback.

var (
	thumbDisks   = make(map[string]*cache.ThumbDisk) // one store per directory, shared by sessions
	thumbDisksMu sync.Mutex
)

func thumbDiskFor(dir string, budget int64) *cache.ThumbDisk {
	thumbDisksMu.Lock()
	defer thumbDisksMu.Unlock()
	if d, ok := thumbDisks[dir]; ok {
		return d
	}
	d := cache.NewThumbDisk(dir, budget)
	thumbDisks[dir] = d
	return d
}

// thumbService returns the session's preview service, creating a memory-only
// one if GocryptfsThumbConfigure was never called.
func (sess *session) thumbService() (*thumb.Service, error) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.thumbs == nil {
		svc, err := thumb.New("", thumb.SessionReader{S: sess.s}, thumb.Options{})
		if err != nil {
			return nil, err
		}
		sess.thumbs = svc
	}
	return sess.thumbs, nil
}

func (sess *session) invalidateThumb(p string) {
	sess.mu.Lock()
	svc := sess.thumbs
	sess.mu.Unlock()
	if svc != nil {
		svc.Invalidate(p)
	}
}

// GocryptfsThumbConfigure sets up previews for a session. sourceID is a stable
// name for the vault (its path, or a gdrive:// URL) that keys the disk tier;
// diskDir may be empty for a memory-only cache. Budgets are in bytes; zero
// picks the defaults (64 MiB memory, 1 GiB disk). Replaces any earlier
// configuration.
//
//export GocryptfsThumbConfigure
func GocryptfsThumbConfigure(sessionID C.int, sourceID *C.char, diskDir *C.char, memBytes C.int64_t, diskBytes C.int64_t) C.int {
	sess := getSession(sessionID)
	if sess == nil {
		return rcNoConfig
	}
	opts := thumb.Options{MemBytes: int64(memBytes)}
	if dir := C.GoString(diskDir); dir != "" {
		budget := int64(diskBytes)
		if budget <= 0 {
			budget = 1 << 30
		}
		opts.Disk = thumbDiskFor(dir, budget)
		opts.Sealer = sess.s
	}
	svc, err := thumb.New(C.GoString(sourceID), thumb.SessionReader{S: sess.s}, opts)
	if err != nil {
		return rcBackend
	}
	sess.mu.Lock()
	if sess.thumbs != nil {
		sess.thumbs.Close()
	}
	sess.thumbs = svc
	sess.mu.Unlock()
	return rcOK
}

// GocryptfsThumbGet returns a JPEG preview whose longest edge is at most
// maxEdge. An empty result with no error means the engine has no decoder for
// this file: the host should render it and call GocryptfsThumbPut.
//
//export GocryptfsThumbGet
func GocryptfsThumbGet(sessionID C.int, relativePath *C.char, maxEdge C.int) C.DecryptResult {
	sess := getSession(sessionID)
	if sess == nil {
		return C.DecryptResult{error: C.CString("invalid session")}
	}
	svc, err := sess.thumbService()
	if err != nil {
		return C.DecryptResult{error: C.CString(err.Error())}
	}
	jpeg, err := svc.Get(context.Background(), C.GoString(relativePath), int(maxEdge))
	if errors.Is(err, thumb.ErrNoPreview) {
		return C.DecryptResult{size: 0}
	}
	if err != nil {
		return C.DecryptResult{error: C.CString(err.Error())}
	}
	// The cache keeps its own sealed copy; the bytes handed to C are the
	// caller's to zero and free.
	return toCResult(append([]byte(nil), jpeg...))
}

// GocryptfsThumbPut stores a preview the host rendered itself.
//
//export GocryptfsThumbPut
func GocryptfsThumbPut(sessionID C.int, relativePath *C.char, maxEdge C.int, jpeg unsafe.Pointer, jpegLen C.int) C.int {
	sess := getSession(sessionID)
	if sess == nil || jpegLen <= 0 {
		return rcNoConfig
	}
	svc, err := sess.thumbService()
	if err != nil {
		return rcBackend
	}
	if err := svc.Put(C.GoString(relativePath), int(maxEdge), C.GoBytes(jpeg, jpegLen)); err != nil {
		return writeCode(err)
	}
	return rcOK
}

// GocryptfsThumbInvalidate drops every cached preview of a path.
//
//export GocryptfsThumbInvalidate
func GocryptfsThumbInvalidate(sessionID C.int, relativePath *C.char) {
	if sess := getSession(sessionID); sess != nil {
		sess.invalidateThumb(C.GoString(relativePath))
	}
}

// GocryptfsThumbCacheClear deletes every preview stored under diskDir, for
// every vault. Entries are sealed per vault, so this is the only way to free
// the space of a vault whose password is gone.
//
//export GocryptfsThumbCacheClear
func GocryptfsThumbCacheClear(diskDir *C.char) C.int {
	dir := C.GoString(diskDir)
	if dir == "" {
		return rcCrypto
	}
	if err := thumbDiskFor(dir, 1<<30).Clear(); err != nil {
		return rcBackend
	}
	return rcOK
}

// GocryptfsThumbCacheSize returns the bytes stored under diskDir.
//
//export GocryptfsThumbCacheSize
func GocryptfsThumbCacheSize(diskDir *C.char) C.int64_t {
	dir := C.GoString(diskDir)
	if dir == "" {
		return 0
	}
	return C.int64_t(thumbDiskFor(dir, 1<<30).Size())
}

// --- Memory helpers --------------------------------------------------------------

// toCResult copies plaintext into C memory and wipes the Go copy.
func toCResult(b []byte) C.DecryptResult {
	defer wipe(b)
	if len(b) == 0 {
		return C.DecryptResult{size: 0}
	}
	cData := C.malloc(C.size_t(len(b)))
	if cData == nil {
		return C.DecryptResult{error: C.CString("malloc failed")}
	}
	C.memcpy(cData, unsafe.Pointer(&b[0]), C.size_t(len(b)))
	return C.DecryptResult{data: cData, size: C.int64_t(len(b))}
}

//export GocryptfsFreeDecryptResult
func GocryptfsFreeDecryptResult(result C.DecryptResult) {
	if result.data != nil {
		// Zero the memory before freeing
		C.memset(result.data, 0, C.size_t(result.size))
		C.free(result.data)
	}
	if result.error != nil {
		C.free(unsafe.Pointer(result.error))
	}
}

//export GocryptfsFreeListResult
func GocryptfsFreeListResult(result C.ListResult) {
	if result.entries != nil {
		slice := unsafe.Slice(result.entries, result.count)
		for _, e := range slice {
			C.free(unsafe.Pointer(e.name))
		}
		C.free(unsafe.Pointer(result.entries))
	}
	if result.error != nil {
		C.free(unsafe.Pointer(result.error))
	}
}

func boolToInt(b bool) C.int {
	if b {
		return 1
	}
	return 0
}

func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

func main() {}

package main

/*
#include <stdlib.h>
#include <stdint.h>
#include <string.h>

typedef struct {
    char* name;
    int is_dir;
    int64_t size;
} DirEntry;

typedef struct {
    DirEntry* entries;
    int count;
    char* error;
} ListResult;

typedef struct {
    void* data;
    int64_t size;
    char* error;
} DecryptResult;

// VFS callbacks — Swift provides these for Drive-backed vaults.
// All paths are vault-relative cipher paths (e.g. "gocryptfs.diriv", "AbCdEf/gocryptfs.diriv").

// Read bytes from an encrypted file. Returns bytes actually read, or -1 on error.
typedef int64_t (*VFSReadAtFn)(void* ctx, const char* path, int64_t offset, int64_t length,
                               uint8_t* buf, int64_t bufSize);
// Return the cipher file size in bytes, or -1 on error.
typedef int64_t (*VFSFileSizeFn)(void* ctx, const char* path);
// List a cipher directory. Returns a ListResult with encrypted entry names.
// The caller (Go) frees the result with GocryptfsFreeListResult.
typedef ListResult (*VFSListDirFn)(void* ctx, const char* path);

// CGo cannot call function pointers directly; these thin wrappers do it for us.
static inline int64_t call_VFSReadAtFn(VFSReadAtFn fn, void* ctx, const char* path,
                                        int64_t offset, int64_t length,
                                        uint8_t* buf, int64_t bufSize) {
    return fn(ctx, path, offset, length, buf, bufSize);
}
static inline int64_t call_VFSFileSizeFn(VFSFileSizeFn fn, void* ctx, const char* path) {
    return fn(ctx, path);
}
static inline ListResult call_VFSListDirFn(VFSListDirFn fn, void* ctx, const char* path) {
    return fn(ctx, path);
}
*/
import "C"

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/adrianm/imagethat/gocryptfs-lib/internal/configfile"
	"github.com/adrianm/imagethat/gocryptfs-lib/internal/contentenc"
	"github.com/adrianm/imagethat/gocryptfs-lib/internal/cryptocore"
	"github.com/adrianm/imagethat/gocryptfs-lib/internal/nametransform"
)

func init() {
	// Write logs to a file for debugging
	logFile, err := os.OpenFile("/tmp/gocryptfs.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err == nil {
		log.SetOutput(logFile)
	}
}

// vaultVFS abstracts file I/O so the same crypto logic works for both local
// vaults (via os.*) and Drive-backed vaults (via C callbacks from Swift).
// All paths are vault-relative cipher paths.
type vaultVFS interface {
	ReadAt(path string, offset int64, buf []byte) (int, error)
	FileSize(path string) (int64, error)
	ReadDir(path string) ([]vfsDirEntry, error)
	ReadFile(path string) ([]byte, error)
}

type vfsDirEntry struct {
	Name  string
	IsDir bool
	Size  int64 // cipher size; 0 for dirs
}

// localVaultFS implements vaultVFS using the local filesystem.
type localVaultFS struct{ rootPath string }

func (v *localVaultFS) abs(path string) string {
	if path == "" || path == "." {
		return v.rootPath
	}
	return filepath.Join(v.rootPath, path)
}

func (v *localVaultFS) ReadAt(path string, offset int64, buf []byte) (int, error) {
	f, err := os.Open(v.abs(path))
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return f.ReadAt(buf, offset)
}

func (v *localVaultFS) FileSize(path string) (int64, error) {
	info, err := os.Stat(v.abs(path))
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func (v *localVaultFS) ReadDir(path string) ([]vfsDirEntry, error) {
	entries, err := os.ReadDir(v.abs(path))
	if err != nil {
		return nil, err
	}
	result := make([]vfsDirEntry, 0, len(entries))
	for _, e := range entries {
		var size int64
		if !e.IsDir() {
			if info, err := e.Info(); err == nil {
				size = info.Size()
			}
		}
		result = append(result, vfsDirEntry{Name: e.Name(), IsDir: e.IsDir(), Size: size})
	}
	return result, nil
}

func (v *localVaultFS) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(v.abs(path))
}

// callbackVaultFS implements vaultVFS via C function pointers provided by Swift.
type callbackVaultFS struct {
	readAtFn   C.VFSReadAtFn
	fileSizeFn C.VFSFileSizeFn
	listDirFn  C.VFSListDirFn
	ctx        unsafe.Pointer
}

func (v *callbackVaultFS) ReadAt(path string, offset int64, buf []byte) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))
	n := C.call_VFSReadAtFn(v.readAtFn, v.ctx, cPath,
		C.int64_t(offset), C.int64_t(len(buf)),
		(*C.uint8_t)(unsafe.Pointer(&buf[0])), C.int64_t(len(buf)))
	if n < 0 {
		return 0, fmt.Errorf("vfs ReadAt %s offset=%d: callback returned %d", path, offset, n)
	}
	return int(n), nil
}

func (v *callbackVaultFS) FileSize(path string) (int64, error) {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))
	sz := C.call_VFSFileSizeFn(v.fileSizeFn, v.ctx, cPath)
	if sz < 0 {
		return 0, fmt.Errorf("vfs FileSize %s: callback returned %d", path, sz)
	}
	return int64(sz), nil
}

func (v *callbackVaultFS) ReadDir(path string) ([]vfsDirEntry, error) {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))
	lr := C.call_VFSListDirFn(v.listDirFn, v.ctx, cPath)
	defer GocryptfsFreeListResult(lr)
	if lr.error != nil {
		return nil, fmt.Errorf("vfs ReadDir %s: %s", path, C.GoString(lr.error))
	}
	count := int(lr.count)
	if count == 0 {
		return nil, nil
	}
	slice := unsafe.Slice(lr.entries, count)
	result := make([]vfsDirEntry, count)
	for i, e := range slice {
		result[i] = vfsDirEntry{
			Name:  C.GoString(e.name),
			IsDir: e.is_dir != 0,
			Size:  int64(e.size),
		}
	}
	return result, nil
}

func (v *callbackVaultFS) ReadFile(path string) ([]byte, error) {
	// Determine file size, then read all bytes via ReadAt.
	sz, err := v.FileSize(path)
	if err != nil {
		return nil, err
	}
	if sz == 0 {
		return []byte{}, nil
	}
	buf := make([]byte, sz)
	n, err := v.ReadAt(path, 0, buf)
	if err != nil && n == 0 {
		return nil, err
	}
	return buf[:n], nil
}

// openFile caches an open file handle for repeated range reads (local vaults only).
type openFile struct {
	f      *os.File
	header *contentenc.FileHeader
	refs   int32 // atomic reference count; file closed when refs hits 0
}

type vaultSession struct {
	fs             vaultVFS
	contentEnc     *contentenc.ContentEnc
	nameTransform  *nametransform.NameTransform
	plaintextNames bool
	useDirIV       bool
	// Cache open file handles for streaming reads (local vault only)
	openFiles   map[string]*openFile
	openFilesMu sync.Mutex
}

var (
	sessions   = make(map[int]*vaultSession)
	sessionMu  sync.RWMutex
	nextID     = 1
)

//export GocryptfsOpen
func GocryptfsOpen(vaultPath *C.char, password *C.char) C.int {
	vPath := C.GoString(vaultPath)
	pass := C.GoString(password)

	configPath := filepath.Join(vPath, configfile.ConfDefaultName)
	cf, err := configfile.Load(configPath)
	if err != nil {
		return -1
	}

	return openSession(cf, pass, &localVaultFS{rootPath: vPath})
}

//export GocryptfsOpenVFS
func GocryptfsOpenVFS(
	configData *C.char, configDataLen C.int,
	password *C.char,
	readAtFn C.VFSReadAtFn, fileSizeFn C.VFSFileSizeFn, listDirFn C.VFSListDirFn,
	ctx unsafe.Pointer,
) C.int {
	data := C.GoBytes(unsafe.Pointer(configData), configDataLen)
	cf, err := parseConfig(data)
	if err != nil {
		log.Printf("[GocryptfsOpenVFS] config parse error: %v", err)
		return -1
	}
	fs := &callbackVaultFS{
		readAtFn:   readAtFn,
		fileSizeFn: fileSizeFn,
		listDirFn:  listDirFn,
		ctx:        ctx,
	}
	return openSession(cf, C.GoString(password), fs)
}

// parseConfig is configfile.Load for a gocryptfs.conf already in memory
// (Drive vaults). Kept here so internal/ stays an unmodified upstream copy.
func parseConfig(data []byte) (*configfile.ConfFile, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("config file is empty")
	}
	var cf configfile.ConfFile
	if err := json.Unmarshal(data, &cf); err != nil {
		return nil, err
	}
	if err := cf.Validate(); err != nil {
		return nil, err
	}
	return &cf, nil
}

// openSession is the shared init path for both local and VFS-backed sessions.
func openSession(cf *configfile.ConfFile, pass string, fs vaultVFS) C.int {
	masterkey, err := cf.DecryptMasterKey([]byte(pass))
	if err != nil {
		return -2 // wrong password
	}

	algo, err := cf.ContentEncryption()
	if err != nil {
		wipeMasterkey(masterkey)
		return -3
	}

	useHKDF := cf.IsFeatureFlagSet(configfile.FlagHKDF)
	cc := cryptocore.New(masterkey, algo, contentenc.DefaultIVBits, useHKDF)
	ce := contentenc.New(cc, contentenc.DefaultBS)

	plaintextNames := cf.IsFeatureFlagSet(configfile.FlagPlaintextNames)
	longNames := cf.IsFeatureFlagSet(configfile.FlagLongNames)
	raw64 := cf.IsFeatureFlagSet(configfile.FlagRaw64)
	useDirIV := cf.IsFeatureFlagSet(configfile.FlagDirIV)

	var nt *nametransform.NameTransform
	if !plaintextNames {
		nt = nametransform.New(cc.EMECipher, longNames, cf.LongNameMax, raw64, nil, !useDirIV)
	}

	wipeMasterkey(masterkey)

	sess := &vaultSession{
		fs:             fs,
		contentEnc:     ce,
		nameTransform:  nt,
		plaintextNames: plaintextNames,
		useDirIV:       useDirIV,
		openFiles:      make(map[string]*openFile),
	}

	sessionMu.Lock()
	id := nextID
	nextID++
	sessions[id] = sess
	sessionMu.Unlock()

	return C.int(id)
}

//export GocryptfsClose
func GocryptfsClose(sessionID C.int) {
	sessionMu.Lock()
	if sess, ok := sessions[int(sessionID)]; ok {
		sess.openFilesMu.Lock()
		for _, of := range sess.openFiles {
			of.release() // release cache's reference
		}
		sess.openFiles = nil
		sess.openFilesMu.Unlock()
	}
	delete(sessions, int(sessionID))
	sessionMu.Unlock()
}

//export GocryptfsEncryptData
func GocryptfsEncryptData(sessionID C.int, plaintext unsafe.Pointer, plaintextLen C.int) C.DecryptResult {
	sess := getSession(int(sessionID))
	if sess == nil {
		return C.DecryptResult{error: C.CString("invalid session")}
	}

	plain := C.GoBytes(plaintext, plaintextLen)

	// Create a file header (random file ID)
	header := contentenc.RandomHeader()
	headerBytes := header.Pack()

	// Encrypt block by block, same as gocryptfs file content
	var ciphertext []byte
	ciphertext = append(ciphertext, headerBytes...)

	plainBS := int(sess.contentEnc.PlainBS())
	var blockNo uint64
	for off := 0; off < len(plain); off += plainBS {
		end := off + plainBS
		if end > len(plain) {
			end = len(plain)
		}
		block := plain[off:end]
		cipherBlock := sess.contentEnc.EncryptBlock(block, blockNo, header.ID)
		ciphertext = append(ciphertext, cipherBlock...)
		blockNo++
	}

	// Copy to C memory
	cData := C.malloc(C.size_t(len(ciphertext)))
	C.memcpy(cData, unsafe.Pointer(&ciphertext[0]), C.size_t(len(ciphertext)))

	return C.DecryptResult{
		data: cData,
		size: C.int64_t(len(ciphertext)),
	}
}

//export GocryptfsDecryptData
func GocryptfsDecryptData(sessionID C.int, ciphertext unsafe.Pointer, ciphertextLen C.int) C.DecryptResult {
	sess := getSession(int(sessionID))
	if sess == nil {
		return C.DecryptResult{error: C.CString("invalid session")}
	}

	cipher := C.GoBytes(ciphertext, ciphertextLen)

	if len(cipher) < contentenc.HeaderLen {
		return C.DecryptResult{error: C.CString("data too short for header")}
	}

	// Parse header
	header, err := contentenc.ParseHeader(cipher[:contentenc.HeaderLen])
	if err != nil {
		return C.DecryptResult{error: C.CString(err.Error())}
	}

	// Decrypt block by block
	var plaintext []byte
	cipherBS := int(sess.contentEnc.CipherBS())
	data := cipher[contentenc.HeaderLen:]
	var blockNo uint64

	for off := 0; off < len(data); off += cipherBS {
		end := off + cipherBS
		if end > len(data) {
			end = len(data)
		}
		block, err := sess.contentEnc.DecryptBlock(data[off:end], blockNo, header.ID)
		if err != nil {
			return C.DecryptResult{error: C.CString(fmt.Sprintf("decrypt block %d: %v", blockNo, err))}
		}
		plaintext = append(plaintext, block...)
		blockNo++
	}

	cData := C.malloc(C.size_t(len(plaintext)))
	C.memcpy(cData, unsafe.Pointer(&plaintext[0]), C.size_t(len(plaintext)))

	// Wipe Go memory
	for i := range plaintext {
		plaintext[i] = 0
	}

	return C.DecryptResult{
		data: cData,
		size: C.int64_t(len(plaintext)),
	}
}

//export GocryptfsListDir
func GocryptfsListDir(sessionID C.int, relativePath *C.char) C.ListResult {
	sess := getSession(int(sessionID))
	if sess == nil {
		return C.ListResult{error: C.CString("invalid session")}
	}

	relPath := C.GoString(relativePath)
	cipherDir := sess.resolveCipherPath(relPath)

	entries, err := sess.fs.ReadDir(cipherDir)
	if err != nil {
		return C.ListResult{error: C.CString(err.Error())}
	}

	// Read directory IV for filename decryption
	var dirIV []byte
	if !sess.plaintextNames && sess.useDirIV {
		dirIV, err = sess.readDirIV(cipherDir)
		if err != nil {
			return C.ListResult{error: C.CString(fmt.Sprintf("failed to read diriv: %v", err))}
		}
	}

	var results []C.DirEntry
	for _, entry := range entries {
		cName := entry.Name

		// Skip special gocryptfs files
		if isSpecialFile(cName) {
			continue
		}

		var plainName string
		if sess.plaintextNames {
			plainName = cName
		} else {
			// Handle long names
			if nametransform.IsLongContent(cName) {
				// Read the actual encrypted name from the .name file
				longNamePath := filepath.Join(cipherDir, cName+nametransform.LongNameSuffix)
				encNameBytes, err := sess.fs.ReadFile(longNamePath)
				if err != nil {
					continue
				}
				plainName, err = sess.nameTransform.DecryptName(string(encNameBytes), dirIV)
				if err != nil {
					continue
				}
			} else {
				plainName, err = sess.nameTransform.DecryptName(cName, dirIV)
				if err != nil {
					continue
				}
			}
		}

		isDir := entry.IsDir
		var size int64
		if !isDir {
			size = int64(sess.contentEnc.CipherSizeToPlainSize(uint64(entry.Size)))
		}

		results = append(results, C.DirEntry{
			name:   C.CString(plainName),
			is_dir: boolToInt(isDir),
			size:   C.int64_t(size),
		})
	}

	if len(results) == 0 {
		return C.ListResult{count: 0}
	}

	cEntries := (*C.DirEntry)(C.malloc(C.size_t(len(results)) * C.size_t(unsafe.Sizeof(C.DirEntry{}))))
	slice := unsafe.Slice(cEntries, len(results))
	copy(slice, results)

	return C.ListResult{
		entries: cEntries,
		count:   C.int(len(results)),
	}
}

//export GocryptfsDecryptFile
func GocryptfsDecryptFile(sessionID C.int, relativePath *C.char) C.DecryptResult {
	sess := getSession(int(sessionID))
	if sess == nil {
		return C.DecryptResult{error: C.CString("invalid session")}
	}

	relPath := C.GoString(relativePath)
	cipherPath := sess.resolveCipherFilePath(relPath)

	// Read entire cipher file via VFS.
	cipher, err := sess.fs.ReadFile(cipherPath)
	if err != nil {
		return C.DecryptResult{error: C.CString(err.Error())}
	}

	if len(cipher) < contentenc.HeaderLen {
		return C.DecryptResult{error: C.CString("file too short for header")}
	}

	header, err := contentenc.ParseHeader(cipher[:contentenc.HeaderLen])
	if err != nil {
		return C.DecryptResult{error: C.CString(err.Error())}
	}

	plainSize := sess.contentEnc.CipherSizeToPlainSize(uint64(len(cipher)))
	plaintext := make([]byte, 0, plainSize)
	cipherBS := int(sess.contentEnc.CipherBS())
	data := cipher[contentenc.HeaderLen:]
	var blockNo uint64

	for off := 0; off < len(data); off += cipherBS {
		end := off + cipherBS
		if end > len(data) {
			end = len(data)
		}
		plain, err2 := sess.contentEnc.DecryptBlock(data[off:end], blockNo, header.ID)
		if err2 != nil {
			return C.DecryptResult{error: C.CString(fmt.Sprintf("decrypt block %d: %v", blockNo, err2))}
		}
		plaintext = append(plaintext, plain...)
		blockNo++
	}

	if len(plaintext) == 0 {
		return C.DecryptResult{size: 0}
	}

	cData := C.malloc(C.size_t(len(plaintext)))
	if cData == nil {
		return C.DecryptResult{error: C.CString("malloc failed")}
	}
	C.memcpy(cData, unsafe.Pointer(&plaintext[0]), C.size_t(len(plaintext)))

	for i := range plaintext {
		plaintext[i] = 0
	}

	return C.DecryptResult{
		data: cData,
		size: C.int64_t(len(plaintext)),
	}
}

//export GocryptfsReadRange
func GocryptfsReadRange(sessionID C.int, relativePath *C.char, offset C.int64_t, length C.int32_t, outBuf unsafe.Pointer) C.int32_t {
	sess := getSession(int(sessionID))
	if sess == nil {
		log.Printf("[GocryptfsReadRange] ERROR: invalid session %d\n", sessionID)
		return -1
	}

	relPath := C.GoString(relativePath)
	log.Printf("[GocryptfsReadRange] Reading %s offset=%d length=%d\n", relPath, offset, length)

	cipherPath := sess.resolveCipherFilePath(relPath)

	plainOff := uint64(offset)
	plainLen := uint64(length)
	cipherBS := sess.contentEnc.CipherBS()
	plainBS := sess.contentEnc.PlainBS()

	const maxInt64 uint64 = 1<<63 - 1
	if plainOff > maxInt64 {
		return 0
	}
	if maxLen := maxInt64 - plainOff; plainLen > maxLen {
		plainLen = maxLen
	}

	firstBlock := plainOff / plainBS
	lastBlock := (plainOff + plainLen - 1) / plainBS

	// Read file header once to get the file ID needed for block decryption.
	header, err := sess.getFileHeader(cipherPath)
	if err != nil {
		log.Printf("[GocryptfsReadRange] ERROR reading header for %s: %v\n", relPath, err)
		return -1
	}

	var result []byte
	blockBuf := make([]byte, cipherBS)

	for blockNo := firstBlock; blockNo <= lastBlock; blockNo++ {
		cipherOff := int64(contentenc.HeaderLen) + int64(blockNo*cipherBS)
		n, err := sess.fs.ReadAt(cipherPath, cipherOff, blockBuf)
		if n == 0 {
			break
		}

		plain, err2 := sess.contentEnc.DecryptBlock(blockBuf[:n], blockNo, header.ID)
		if err2 != nil {
			log.Printf("[GocryptfsReadRange] Block %d: DecryptBlock failed: %v\n", blockNo, err2)
			return -1
		}

		blockPlainOff := blockNo * plainBS
		startInBlock := uint64(0)
		if plainOff > blockPlainOff {
			startInBlock = plainOff - blockPlainOff
		}
		endInBlock := uint64(len(plain))
		if blockPlainOff+endInBlock > plainOff+plainLen {
			endInBlock = plainOff + plainLen - blockPlainOff
		}
		if endInBlock > uint64(len(plain)) {
			endInBlock = uint64(len(plain))
		}
		if startInBlock < endInBlock {
			result = append(result, plain[startInBlock:endInBlock]...)
		}

		if err != nil {
			break
		}
	}

	if len(result) == 0 {
		log.Printf("[GocryptfsReadRange] returned 0 bytes for offset=%d length=%d\n", offset, length)
		return 0
	}

	log.Printf("[GocryptfsReadRange] OK: read %d bytes (requested %d)\n", len(result), length)
	C.memcpy(outBuf, unsafe.Pointer(&result[0]), C.size_t(len(result)))
	for i := range result {
		result[i] = 0
	}

	return C.int32_t(len(result))
}

//export GocryptfsFileSize
func GocryptfsFileSize(sessionID C.int, relativePath *C.char) C.int64_t {
	sess := getSession(int(sessionID))
	if sess == nil {
		return -1
	}

	relPath := C.GoString(relativePath)
	cipherPath := sess.resolveCipherFilePath(relPath)

	sz, err := sess.fs.FileSize(cipherPath)
	if err != nil {
		return -1
	}

	return C.int64_t(sess.contentEnc.CipherSizeToPlainSize(uint64(sz)))
}

//export GocryptfsEncryptFileName
func GocryptfsEncryptFileName(sessionID C.int, cipherDirPath *C.char, plainName *C.char) *C.char {
	sess := getSession(int(sessionID))
	if sess == nil {
		return nil
	}
	if sess.plaintextNames {
		return C.CString(C.GoString(plainName))
	}
	dirPath := C.GoString(cipherDirPath)
	name := C.GoString(plainName)
	var dirIV []byte
	if sess.useDirIV {
		var err error
		dirIV, err = sess.readDirIV(dirPath)
		if err != nil {
			return nil
		}
	}
	encName, err := sess.nameTransform.EncryptAndHashName(name, dirIV)
	if err != nil {
		return nil
	}
	return C.CString(encName)
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

// --- Internal helpers ---

func getSession(id int) *vaultSession {
	sessionMu.RLock()
	defer sessionMu.RUnlock()
	return sessions[id]
}

func (s *vaultSession) resolveCipherPath(plainRelPath string) string {
	if s.plaintextNames {
		return plainRelPath
	}

	// Walk the path components, encrypting each one.
	// All paths are kept relative for both local and VFS backends.
	parts := splitPath(plainRelPath)
	current := ""
	for _, part := range parts {
		var dirIV []byte
		if s.useDirIV {
			var err error
			dirIV, err = s.readDirIV(current)
			if err != nil {
				return plainRelPath
			}
		}
		encName, err := s.nameTransform.EncryptName(part, dirIV)
		if err != nil {
			return plainRelPath
		}
		if current == "" {
			current = encName
		} else {
			current = filepath.Join(current, encName)
		}
	}
	return current
}

func (s *vaultSession) resolveCipherFilePath(plainRelPath string) string {
	dir := filepath.Dir(plainRelPath)
	name := filepath.Base(plainRelPath)

	var cipherDir string
	if dir == "." || dir == "" {
		cipherDir = ""
	} else {
		cipherDir = s.resolveCipherPath(dir)
	}

	if s.plaintextNames {
		if cipherDir == "" {
			return name
		}
		return filepath.Join(cipherDir, name)
	}

	var dirIV []byte
	if s.useDirIV {
		var err error
		dirIV, err = s.readDirIV(cipherDir)
		if err != nil {
			if cipherDir == "" {
				return name
			}
			return filepath.Join(cipherDir, name)
		}
	}

	encName, err := s.nameTransform.EncryptName(name, dirIV)
	if err != nil {
		if cipherDir == "" {
			return name
		}
		return filepath.Join(cipherDir, name)
	}

	var cipherFilePath string
	if cipherDir == "" {
		cipherFilePath = encName
	} else {
		cipherFilePath = filepath.Join(cipherDir, encName)
	}

	// Check normal path; if missing try long-name hash.
	if _, err := s.fs.FileSize(cipherFilePath); err == nil {
		return cipherFilePath
	}

	hashName := s.nameTransform.HashLongName(encName)
	var longPath string
	if cipherDir == "" {
		longPath = hashName
	} else {
		longPath = filepath.Join(cipherDir, hashName)
	}
	if _, err := s.fs.FileSize(longPath); err == nil {
		return longPath
	}

	return cipherFilePath
}

// getFileHeader reads and parses the 18-byte gocryptfs file header.
// For local vaults the open-file cache is used to avoid redundant syscalls.
func (s *vaultSession) getFileHeader(cipherPath string) (*contentenc.FileHeader, error) {
	// Try the open-file cache first (local vaults only).
	s.openFilesMu.Lock()
	if of, ok := s.openFiles[cipherPath]; ok {
		h := of.header
		s.openFilesMu.Unlock()
		return h, nil
	}
	s.openFilesMu.Unlock()

	// For local vaults, open and cache the file handle.
	if lfs, ok := s.fs.(*localVaultFS); ok {
		f, err := os.Open(lfs.abs(cipherPath))
		if err != nil {
			return nil, err
		}
		headerBuf := make([]byte, contentenc.HeaderLen)
		if _, err := f.Read(headerBuf); err != nil {
			f.Close()
			return nil, err
		}
		header, err := contentenc.ParseHeader(headerBuf)
		if err != nil {
			f.Close()
			return nil, err
		}
		of := &openFile{f: f, header: header, refs: 2}

		s.openFilesMu.Lock()
		s.openFiles[cipherPath] = of
		if len(s.openFiles) > 16 {
			for k, v := range s.openFiles {
				if k != cipherPath {
					v.release()
					delete(s.openFiles, k)
					break
				}
			}
		}
		s.openFilesMu.Unlock()
		return header, nil
	}

	// VFS backend: just read the header bytes directly.
	headerBuf := make([]byte, contentenc.HeaderLen)
	n, err := s.fs.ReadAt(cipherPath, 0, headerBuf)
	if err != nil || n < contentenc.HeaderLen {
		return nil, fmt.Errorf("read header: %v", err)
	}
	return contentenc.ParseHeader(headerBuf)
}

// retain increments the reference count. Call release() when done.
func (of *openFile) retain() {
	atomic.AddInt32(&of.refs, 1)
}

// release decrements the reference count, closing the file when it reaches 0.
func (of *openFile) release() {
	if atomic.AddInt32(&of.refs, -1) == 0 {
		of.f.Close()
	}
}

func (s *vaultSession) readDirIV(cipherDirPath string) ([]byte, error) {
	var ivPath string
	if cipherDirPath == "" || cipherDirPath == "." {
		ivPath = nametransform.DirIVFilename
	} else {
		ivPath = filepath.Join(cipherDirPath, nametransform.DirIVFilename)
	}
	iv, err := s.fs.ReadFile(ivPath)
	if err != nil {
		return nil, err
	}
	if len(iv) != nametransform.DirIVLen {
		return nil, fmt.Errorf("diriv: wrong length %d", len(iv))
	}
	return iv, nil
}

func isSpecialFile(name string) bool {
	return name == configfile.ConfDefaultName ||
		name == nametransform.DirIVFilename ||
		strings.HasSuffix(name, nametransform.LongNameSuffix)
}

func splitPath(p string) []string {
	p = filepath.Clean(p)
	if p == "." || p == "" {
		return nil
	}
	return strings.Split(p, string(filepath.Separator))
}

func boolToInt(b bool) C.int {
	if b {
		return 1
	}
	return 0
}

func wipeMasterkey(key []byte) {
	for i := range key {
		key[i] = 0
	}
}

// unused import guard
var _ = base64.URLEncoding

// localFS returns the *localVaultFS for write operations, or nil if this is a VFS session.
func (s *vaultSession) localFS() *localVaultFS {
	lfs, _ := s.fs.(*localVaultFS)
	return lfs
}

//export GocryptfsRenameFile
func GocryptfsRenameFile(sessionID C.int, sourcePath *C.char, destDirPath *C.char) C.int {
	sess := getSession(int(sessionID))
	if sess == nil {
		return -1
	}
	lfs := sess.localFS()
	if lfs == nil {
		return -1 // write ops not supported on VFS sessions
	}

	srcRel := C.GoString(sourcePath)
	dstDirRel := C.GoString(destDirPath)
	plainName := filepath.Base(srcRel)

	srcCipherRel := sess.resolveCipherFilePath(srcRel)
	dstCipherDirRel := sess.resolveCipherPath(dstDirRel)
	srcCipherPath := lfs.abs(srcCipherRel)
	dstCipherDir := lfs.abs(dstCipherDirRel)

	if _, err := os.Stat(srcCipherPath); err != nil {
		return -2
	}

	srcIsLong := nametransform.IsLongContent(filepath.Base(srcCipherPath))

	var dstCipherPath string
	var dstIsLong bool
	var dstDirIV []byte

	if sess.plaintextNames {
		dstCipherPath = filepath.Join(dstCipherDir, plainName)
	} else {
		if sess.useDirIV {
			var err error
			dstDirIV, err = sess.readDirIV(dstCipherDirRel)
			if err != nil {
				return -3
			}
		}
		dstEncName, err := sess.nameTransform.EncryptAndHashName(plainName, dstDirIV)
		if err != nil {
			return -4
		}
		dstCipherPath = filepath.Join(dstCipherDir, dstEncName)
		dstIsLong = nametransform.IsLongContent(dstEncName)
	}

	if err := os.Rename(srcCipherPath, dstCipherPath); err != nil {
		return -5
	}

	if !sess.plaintextNames {
		if dstIsLong {
			fullEncName, _ := sess.nameTransform.EncryptName(plainName, dstDirIV)
			_ = os.WriteFile(dstCipherPath+nametransform.LongNameSuffix, []byte(fullEncName), 0600)
		}
		if srcIsLong {
			_ = os.Remove(srcCipherPath + nametransform.LongNameSuffix)
		}
	}

	sess.openFilesMu.Lock()
	if of, ok := sess.openFiles[srcCipherRel]; ok {
		of.release()
		delete(sess.openFiles, srcCipherRel)
	}
	sess.openFilesMu.Unlock()

	return 0
}

//export GocryptfsMkdir
func GocryptfsMkdir(sessionID C.int, dirPath *C.char) C.int {
	sess := getSession(int(sessionID))
	if sess == nil {
		return -1
	}
	lfs := sess.localFS()
	if lfs == nil {
		return -1
	}

	dirRel := C.GoString(dirPath)
	parentRel := filepath.Dir(dirRel)
	plainName := filepath.Base(dirRel)

	cipherParentRel := sess.resolveCipherPath(parentRel)
	cipherParentAbs := lfs.abs(cipherParentRel)

	var cipherName string
	if sess.plaintextNames {
		cipherName = plainName
	} else {
		var dirIV []byte
		if sess.useDirIV {
			var err error
			dirIV, err = sess.readDirIV(cipherParentRel)
			if err != nil {
				return -2
			}
		}
		var err error
		cipherName, err = sess.nameTransform.EncryptAndHashName(plainName, dirIV)
		if err != nil {
			return -3
		}
	}

	newCipherDir := filepath.Join(cipherParentAbs, cipherName)
	if err := os.Mkdir(newCipherDir, 0700); err != nil {
		return -4
	}

	if !sess.plaintextNames && sess.useDirIV {
		newDirIV := make([]byte, nametransform.DirIVLen)
		if _, err := rand.Read(newDirIV); err != nil {
			_ = os.Remove(newCipherDir)
			return -5
		}
		ivPath := filepath.Join(newCipherDir, nametransform.DirIVFilename)
		if err := os.WriteFile(ivPath, newDirIV, 0400); err != nil {
			_ = os.Remove(newCipherDir)
			return -5
		}
	}

	return 0
}

//export GocryptfsDeleteFile
func GocryptfsDeleteFile(sessionID C.int, filePath *C.char) C.int {
	sess := getSession(int(sessionID))
	if sess == nil {
		return -1
	}
	lfs := sess.localFS()
	if lfs == nil {
		return -1
	}

	relPath := C.GoString(filePath)
	cipherRel := sess.resolveCipherFilePath(relPath)
	cipherAbs := lfs.abs(cipherRel)

	if _, err := os.Stat(cipherAbs); err != nil {
		return -2
	}

	isLong := nametransform.IsLongContent(filepath.Base(cipherAbs))

	sess.openFilesMu.Lock()
	if of, ok := sess.openFiles[cipherRel]; ok {
		of.release()
		delete(sess.openFiles, cipherRel)
	}
	sess.openFilesMu.Unlock()

	if err := os.Remove(cipherAbs); err != nil {
		return -3
	}

	if isLong {
		_ = os.Remove(cipherAbs + nametransform.LongNameSuffix)
	}

	return 0
}

//export GocryptfsDeleteDir
func GocryptfsDeleteDir(sessionID C.int, dirPath *C.char) C.int {
	sess := getSession(int(sessionID))
	if sess == nil {
		return -1
	}
	lfs := sess.localFS()
	if lfs == nil {
		return -1
	}

	dirRel := C.GoString(dirPath)
	cipherRel := sess.resolveCipherPath(dirRel)
	cipherAbs := lfs.abs(cipherRel)

	if _, err := os.Stat(cipherAbs); err != nil {
		return -2
	}

	if err := os.RemoveAll(cipherAbs); err != nil {
		return -3
	}

	return 0
}

//export GocryptfsWriteFile
func GocryptfsWriteFile(sessionID C.int, relativePath *C.char, data unsafe.Pointer, dataLen C.int) C.int {
	sess := getSession(int(sessionID))
	if sess == nil {
		return -1
	}
	lfs := sess.localFS()
	if lfs == nil {
		return -1
	}

	relPath := C.GoString(relativePath)
	plainData := C.GoBytes(data, dataLen)

	cipherRel := sess.resolveCipherFilePath(relPath)
	cipherAbs := lfs.abs(cipherRel)
	cipherDirRel := filepath.Dir(cipherRel)
	cipherDirAbs := filepath.Dir(cipherAbs)

	if err := os.MkdirAll(cipherDirAbs, 0700); err != nil {
		return -2
	}

	header := contentenc.RandomHeader()
	headerBytes := header.Pack()

	var ciphertext []byte
	ciphertext = append(ciphertext, headerBytes...)

	plainBS := int(sess.contentEnc.PlainBS())
	var blockNo uint64
	for off := 0; off < len(plainData); off += plainBS {
		end := off + plainBS
		if end > len(plainData) {
			end = len(plainData)
		}
		cipherBlock := sess.contentEnc.EncryptBlock(plainData[off:end], blockNo, header.ID)
		ciphertext = append(ciphertext, cipherBlock...)
		blockNo++
	}

	tempPath := cipherAbs + ".tmp"
	if err := os.WriteFile(tempPath, ciphertext, 0600); err != nil {
		return -3
	}

	if err := os.Rename(tempPath, cipherAbs); err != nil {
		_ = os.Remove(tempPath)
		return -4
	}

	if nametransform.IsLongContent(filepath.Base(cipherAbs)) {
		plainName := filepath.Base(relPath)
		var dirIV []byte
		if sess.useDirIV {
			var err error
			dirIV, err = sess.readDirIV(cipherDirRel)
			if err != nil {
				return -5
			}
		}
		fullEncName, err := sess.nameTransform.EncryptName(plainName, dirIV)
		if err != nil {
			return -6
		}
		if err := os.WriteFile(cipherAbs+nametransform.LongNameSuffix, []byte(fullEncName), 0600); err != nil {
			return -7
		}
	}

	sess.openFilesMu.Lock()
	if of, ok := sess.openFiles[cipherRel]; ok {
		of.release()
		delete(sess.openFiles, cipherRel)
	}
	sess.openFilesMu.Unlock()

	for i := range plainData {
		plainData[i] = 0
	}

	return 0
}

//export GocryptfsResolveCipherPath
func GocryptfsResolveCipherPath(sessionID C.int, plainRelPath *C.char, isDir C.int) *C.char {
	sess := getSession(int(sessionID))
	if sess == nil {
		return nil
	}
	relPath := C.GoString(plainRelPath)
	var cipherPath string
	if isDir != 0 {
		cipherPath = sess.resolveCipherPath(relPath)
	} else {
		cipherPath = sess.resolveCipherFilePath(relPath)
	}
	return C.CString(cipherPath)
}

func main() {}

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
*/
import "C"

import (
	"crypto/rand"
	"encoding/base64"
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

type openFile struct {
	f      *os.File
	header *contentenc.FileHeader
	refs   int32 // atomic reference count; file closed when refs hits 0
}

type vaultSession struct {
	rootPath       string
	contentEnc     *contentenc.ContentEnc
	nameTransform  *nametransform.NameTransform
	plaintextNames bool
	useDirIV       bool
	// Cache open file handles for streaming reads
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
		rootPath:       vPath,
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

	entries, err := os.ReadDir(cipherDir)
	if err != nil {
		return C.ListResult{error: C.CString(err.Error())}
	}

	// Read directory IV for filename decryption
	var dirIV []byte
	if !sess.plaintextNames && sess.useDirIV {
		dirIV, err = readDirIV(cipherDir)
		if err != nil {
			return C.ListResult{error: C.CString(fmt.Sprintf("failed to read diriv: %v", err))}
		}
	}

	var results []C.DirEntry
	for _, entry := range entries {
		cName := entry.Name()

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
				encNameBytes, err := os.ReadFile(longNamePath)
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

		info, err := entry.Info()
		isDir := entry.IsDir()
		var size int64
		if err == nil && !isDir {
			cipherSize := uint64(info.Size())
			size = int64(sess.contentEnc.CipherSizeToPlainSize(cipherSize))
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

	f, err := os.Open(cipherPath)
	if err != nil {
		return C.DecryptResult{error: C.CString(err.Error())}
	}
	defer f.Close()

	// Read file header
	headerBuf := make([]byte, contentenc.HeaderLen)
	n, err := f.Read(headerBuf)
	if err != nil || n != contentenc.HeaderLen {
		return C.DecryptResult{error: C.CString("failed to read file header")}
	}

	header, err := contentenc.ParseHeader(headerBuf)
	if err != nil {
		return C.DecryptResult{error: C.CString(err.Error())}
	}

	// Get file size to calculate plaintext size
	stat, err := f.Stat()
	if err != nil {
		return C.DecryptResult{error: C.CString(err.Error())}
	}
	plainSize := sess.contentEnc.CipherSizeToPlainSize(uint64(stat.Size()))

	// Read and decrypt all blocks
	plaintext := make([]byte, 0, plainSize)
	cipherBS := int(sess.contentEnc.CipherBS())
	blockBuf := make([]byte, cipherBS)
	var blockNo uint64

	for {
		n, err := f.Read(blockBuf)
		if n == 0 {
			break
		}

		plain, err2 := sess.contentEnc.DecryptBlock(blockBuf[:n], blockNo, header.ID)
		if err2 != nil {
			return C.DecryptResult{error: C.CString(fmt.Sprintf("decrypt block %d: %v", blockNo, err2))}
		}
		plaintext = append(plaintext, plain...)
		blockNo++

		if err != nil {
			break
		}
	}

	// Copy to C memory
	cData := C.malloc(C.size_t(len(plaintext)))
	if cData == nil {
		return C.DecryptResult{error: C.CString("malloc failed")}
	}
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

//export GocryptfsReadRange
func GocryptfsReadRange(sessionID C.int, relativePath *C.char, offset C.int64_t, length C.int32_t, outBuf unsafe.Pointer) C.int32_t {
	sess := getSession(int(sessionID))
	if sess == nil {
		log.Printf("[GocryptfsReadRange] ERROR: invalid session %d\n", sessionID)
		return -1
	}

	relPath := C.GoString(relativePath)
	log.Printf("[GocryptfsReadRange] Reading %s offset=%d length=%d\n", relPath, offset, length)

	of, err := sess.getOrOpenFile(relPath)
	if err != nil {
		log.Printf("[GocryptfsReadRange] ERROR opening file: %v\n", err)
		return -1
	}
	defer of.release()

	plainOff := uint64(offset)
	plainLen := uint64(length)
	cipherBS := sess.contentEnc.CipherBS()
	plainBS := sess.contentEnc.PlainBS()

	// Cap plainLen to prevent overflow in plainOff + plainLen arithmetic
	// Max safe read is INT64_MAX - plainOff
	const maxInt64 uint64 = 1<<63 - 1
	if plainOff > maxInt64 {
		return 0 // Offset beyond valid range
	}
	maxLen := maxInt64 - plainOff
	if plainLen > maxLen {
		plainLen = maxLen
	}

	firstBlock := plainOff / plainBS
	lastBlock := (plainOff + plainLen - 1) / plainBS

	var result []byte
	blockBuf := make([]byte, cipherBS)

	for blockNo := firstBlock; blockNo <= lastBlock; blockNo++ {
		cipherOff := int64(contentenc.HeaderLen + blockNo*cipherBS)
		n, err := of.f.ReadAt(blockBuf, cipherOff)
		if n == 0 {
			log.Printf("[GocryptfsReadRange] Block %d: ReadAt returned 0 bytes\n", blockNo)
			break
		}
		if err != nil && n == 0 {
			log.Printf("[GocryptfsReadRange] Block %d: ReadAt error: %v\n", blockNo, err)
			break
		}

		plain, err2 := sess.contentEnc.DecryptBlock(blockBuf[:n], blockNo, of.header.ID)
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

		if startInBlock < endInBlock && startInBlock < uint64(len(plain)) {
			if endInBlock > uint64(len(plain)) {
				endInBlock = uint64(len(plain))
			}
			result = append(result, plain[startInBlock:endInBlock]...)
		}

		if err != nil {
			break
		}
	}

	if len(result) == 0 {
		log.Printf("[GocryptfsReadRange] ERROR: read returned 0 bytes for offset=%d length=%d\n", offset, length)
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

	stat, err := os.Stat(cipherPath)
	if err != nil {
		return -1
	}

	return C.int64_t(sess.contentEnc.CipherSizeToPlainSize(uint64(stat.Size())))
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
		return filepath.Join(s.rootPath, plainRelPath)
	}

	// Walk the path components, encrypting each one
	parts := splitPath(plainRelPath)
	current := s.rootPath
	for _, part := range parts {
		var dirIV []byte
		if s.useDirIV {
			var err error
			dirIV, err = readDirIV(current)
			if err != nil {
				return filepath.Join(s.rootPath, plainRelPath)
			}
		}
		encName, err := s.nameTransform.EncryptName(part, dirIV)
		if err != nil {
			return filepath.Join(s.rootPath, plainRelPath)
		}
		current = filepath.Join(current, encName)
	}
	return current
}

func (s *vaultSession) resolveCipherFilePath(plainRelPath string) string {
	dir := filepath.Dir(plainRelPath)
	name := filepath.Base(plainRelPath)

	var cipherDir string
	if dir == "." || dir == "" {
		cipherDir = s.rootPath
	} else {
		cipherDir = s.resolveCipherPath(dir)
	}

	if s.plaintextNames {
		return filepath.Join(cipherDir, name)
	}

	var dirIV []byte
	if s.useDirIV {
		var err error
		dirIV, err = readDirIV(cipherDir)
		if err != nil {
			return filepath.Join(cipherDir, name)
		}
	}

	encName, err := s.nameTransform.EncryptName(name, dirIV)
	if err != nil {
		return filepath.Join(cipherDir, name)
	}

	// Check if it's a long name
	cipherFilePath := filepath.Join(cipherDir, encName)
	if _, err := os.Stat(cipherFilePath); err == nil {
		return cipherFilePath
	}

	// Try long name hash
	hashName := s.nameTransform.HashLongName(encName)
	longPath := filepath.Join(cipherDir, hashName)
	if _, err := os.Stat(longPath); err == nil {
		return longPath
	}

	return cipherFilePath
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

// getOrOpenFile returns a retained *openFile. The caller MUST call release() when done.
func (s *vaultSession) getOrOpenFile(relPath string) (*openFile, error) {
	cipherPath := s.resolveCipherFilePath(relPath)

	s.openFilesMu.Lock()
	defer s.openFilesMu.Unlock()

	if of, ok := s.openFiles[cipherPath]; ok {
		of.retain() // caller's reference
		return of, nil
	}

	f, err := os.Open(cipherPath)
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

	// refs=2: one for the cache, one for the caller
	of := &openFile{f: f, header: header, refs: 2}
	s.openFiles[cipherPath] = of

	// Limit cache size — evict oldest if > 16 open files
	if len(s.openFiles) > 16 {
		for k, v := range s.openFiles {
			if k != cipherPath {
				v.release()             // release cache's reference
				delete(s.openFiles, k)
				break
			}
		}
	}

	return of, nil
}

func readDirIV(dirPath string) ([]byte, error) {
	ivPath := filepath.Join(dirPath, nametransform.DirIVFilename)
	iv, err := os.ReadFile(ivPath)
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

//export GocryptfsRenameFile
func GocryptfsRenameFile(sessionID C.int, sourcePath *C.char, destDirPath *C.char) C.int {
	sess := getSession(int(sessionID))
	if sess == nil {
		return -1
	}

	srcRel := C.GoString(sourcePath)
	dstDirRel := C.GoString(destDirPath)
	plainName := filepath.Base(srcRel)

	srcCipherPath := sess.resolveCipherFilePath(srcRel)
	dstCipherDir := sess.resolveCipherPath(dstDirRel)

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
			dstDirIV, err = readDirIV(dstCipherDir)
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
	if of, ok := sess.openFiles[srcCipherPath]; ok {
		of.release()
		delete(sess.openFiles, srcCipherPath)
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

	dirRel := C.GoString(dirPath)

	cipherDirPath := sess.resolveCipherPath(filepath.Dir(dirRel))
	plainName := filepath.Base(dirRel)

	var cipherName string
	if sess.plaintextNames {
		cipherName = plainName
	} else {
		var dirIV []byte
		if sess.useDirIV {
			var err error
			dirIV, err = readDirIV(cipherDirPath)
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

	newCipherDir := filepath.Join(cipherDirPath, cipherName)
	if err := os.Mkdir(newCipherDir, 0700); err != nil {
		return -4
	}

	// Write diriv for the new directory
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

	relPath := C.GoString(filePath)
	cipherPath := sess.resolveCipherFilePath(relPath)

	if _, err := os.Stat(cipherPath); err != nil {
		return -2
	}

	isLong := nametransform.IsLongContent(filepath.Base(cipherPath))

	// Evict from open file cache
	sess.openFilesMu.Lock()
	if of, ok := sess.openFiles[cipherPath]; ok {
		of.release()
		delete(sess.openFiles, cipherPath)
	}
	sess.openFilesMu.Unlock()

	if err := os.Remove(cipherPath); err != nil {
		return -3
	}

	if isLong {
		_ = os.Remove(cipherPath + nametransform.LongNameSuffix)
	}

	return 0
}

//export GocryptfsDeleteDir
func GocryptfsDeleteDir(sessionID C.int, dirPath *C.char) C.int {
	sess := getSession(int(sessionID))
	if sess == nil {
		return -1
	}

	dirRel := C.GoString(dirPath)
	cipherDirPath := sess.resolveCipherPath(dirRel)

	if _, err := os.Stat(cipherDirPath); err != nil {
		return -2
	}

	if err := os.RemoveAll(cipherDirPath); err != nil {
		return -3
	}

	return 0
}

//export GocryptfsWriteFile
func GocryptfsWriteFile(sessionID C.int, relativePath *C.char, data unsafe.Pointer, dataLen C.int) C.int {
	sess := getSession(int(sessionID))
	if sess == nil {
		return -1 // invalid session
	}

	relPath := C.GoString(relativePath)
	plainData := C.GoBytes(data, dataLen)

	// Resolve the cipher path for the destination file
	cipherPath := sess.resolveCipherFilePath(relPath)
	cipherDir := filepath.Dir(cipherPath)

	// Create parent directories if they don't exist
	if err := os.MkdirAll(cipherDir, 0700); err != nil {
		return -2 // failed to create parent directories
	}

	// Create a file header with a random file ID
	header := contentenc.RandomHeader()
	headerBytes := header.Pack()

	// Encrypt the plaintext block-by-block
	var ciphertext []byte
	ciphertext = append(ciphertext, headerBytes...)

	plainBS := int(sess.contentEnc.PlainBS())
	var blockNo uint64
	for off := 0; off < len(plainData); off += plainBS {
		end := off + plainBS
		if end > len(plainData) {
			end = len(plainData)
		}
		block := plainData[off:end]
		cipherBlock := sess.contentEnc.EncryptBlock(block, blockNo, header.ID)
		ciphertext = append(ciphertext, cipherBlock...)
		blockNo++
	}

	// Write atomically: write to temp file, then rename to target
	tempPath := cipherPath + ".tmp"
	if err := os.WriteFile(tempPath, ciphertext, 0600); err != nil {
		return -3 // failed to write temp file
	}

	if err := os.Rename(tempPath, cipherPath); err != nil {
		_ = os.Remove(tempPath)
		return -4 // failed to rename temp file
	}

	// Handle long-name sidecar if the encrypted filename is long
	if nametransform.IsLongContent(filepath.Base(cipherPath)) {
		plainName := filepath.Base(relPath)
		var dirIV []byte
		if sess.useDirIV {
			var err error
			dirIV, err = readDirIV(cipherDir)
			if err != nil {
				return -5 // failed to read diriv for long-name sidecar
			}
		}
		fullEncName, err := sess.nameTransform.EncryptName(plainName, dirIV)
		if err != nil {
			return -6 // failed to encrypt name for sidecar
		}
		sidecarPath := cipherPath + nametransform.LongNameSuffix
		if err := os.WriteFile(sidecarPath, []byte(fullEncName), 0600); err != nil {
			return -7 // failed to write long-name sidecar
		}
	}

	// Evict from open file cache if it exists (file was just overwritten)
	sess.openFilesMu.Lock()
	if of, ok := sess.openFiles[cipherPath]; ok {
		of.release()
		delete(sess.openFiles, cipherPath)
	}
	sess.openFilesMu.Unlock()

	// Wipe plaintext from memory
	for i := range plainData {
		plainData[i] = 0
	}

	return 0 // success
}

func main() {}

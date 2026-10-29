//go:build shimtest

// Test-only Go wrappers over the C exports. _test.go files cannot import "C",
// so main_test.go drives the shim through these; the shimtest build tag keeps
// them out of libgocryptfs.dylib.
package main

/*
#include <stdlib.h>
#include "shim.h"
*/
import "C"

import (
	"errors"
	"unsafe"
)

type tEntry struct {
	Name  string
	IsDir bool
	Size  int64
}

func cstr(s string) *C.char { return C.CString(s) }

func tOpen(dir, password string) int {
	d, p := cstr(dir), cstr(password)
	defer C.free(unsafe.Pointer(d))
	defer C.free(unsafe.Pointer(p))
	return int(GocryptfsOpen(d, p))
}

func tClose(id int) { GocryptfsClose(C.int(id)) }

func tList(id int, path string) ([]tEntry, error) {
	p := cstr(path)
	defer C.free(unsafe.Pointer(p))
	r := GocryptfsListDir(C.int(id), p)
	defer GocryptfsFreeListResult(r)
	if r.error != nil {
		return nil, errors.New(C.GoString(r.error))
	}
	if r.count == 0 {
		return nil, nil
	}
	out := make([]tEntry, int(r.count))
	for i, e := range unsafe.Slice(r.entries, int(r.count)) {
		out[i] = tEntry{Name: C.GoString(e.name), IsDir: e.is_dir != 0, Size: int64(e.size)}
	}
	return out, nil
}

func tFileSize(id int, path string) int64 {
	p := cstr(path)
	defer C.free(unsafe.Pointer(p))
	return int64(GocryptfsFileSize(C.int(id), p))
}

func tReadRange(id int, path string, off int64, n int32) ([]byte, int32) {
	p := cstr(path)
	defer C.free(unsafe.Pointer(p))
	buf := make([]byte, int(n)+1) // +1 so a zero-length request still has an address
	got := GocryptfsReadRange(C.int(id), p, C.int64_t(off), C.int32_t(n), unsafe.Pointer(&buf[0]))
	if got < 0 {
		return nil, int32(got)
	}
	return buf[:int(got)], int32(got)
}

func fromResult(r C.DecryptResult) ([]byte, error) {
	defer GocryptfsFreeDecryptResult(r)
	if r.error != nil {
		return nil, errors.New(C.GoString(r.error))
	}
	if r.size == 0 {
		return []byte{}, nil
	}
	return C.GoBytes(r.data, C.int(r.size)), nil
}

func tDecryptFile(id int, path string) ([]byte, error) {
	p := cstr(path)
	defer C.free(unsafe.Pointer(p))
	return fromResult(GocryptfsDecryptFile(C.int(id), p))
}

// ptr returns the address of b's first byte, or nil for an empty slice, the
// way a C caller passes a buffer.
func ptr(b []byte) unsafe.Pointer {
	if len(b) == 0 {
		return nil
	}
	return unsafe.Pointer(&b[0])
}

func tEncryptData(id int, plain []byte) ([]byte, error) {
	return fromResult(GocryptfsEncryptData(C.int(id), ptr(plain), C.int(len(plain))))
}

func tDecryptData(id int, cipher []byte) ([]byte, error) {
	return fromResult(GocryptfsDecryptData(C.int(id), ptr(cipher), C.int(len(cipher))))
}

func tWriteFile(id int, path string, data []byte) int {
	p := cstr(path)
	defer C.free(unsafe.Pointer(p))
	return int(GocryptfsWriteFile(C.int(id), p, ptr(data), C.int(len(data))))
}

func tCreate(id int, path string) int {
	p := cstr(path)
	defer C.free(unsafe.Pointer(p))
	return int(GocryptfsCreateFile(C.int(id), p))
}

func tWrite(wid int, data []byte) int {
	return int(GocryptfsWriterWrite(C.int(wid), ptr(data), C.int(len(data))))
}

func tWriterClose(wid int) int { return int(GocryptfsWriterClose(C.int(wid))) }
func tWriterAbort(wid int)     { GocryptfsWriterAbort(C.int(wid)) }

func tRename(id int, src, dstDir string) int {
	s, d := cstr(src), cstr(dstDir)
	defer C.free(unsafe.Pointer(s))
	defer C.free(unsafe.Pointer(d))
	return int(GocryptfsRenameFile(C.int(id), s, d))
}

func tMkdir(id int, path string) int {
	p := cstr(path)
	defer C.free(unsafe.Pointer(p))
	return int(GocryptfsMkdir(C.int(id), p))
}

func tDeleteFile(id int, path string) int {
	p := cstr(path)
	defer C.free(unsafe.Pointer(p))
	return int(GocryptfsDeleteFile(C.int(id), p))
}

func tDeleteDir(id int, path string) int {
	p := cstr(path)
	defer C.free(unsafe.Pointer(p))
	return int(GocryptfsDeleteDir(C.int(id), p))
}

func tResolve(id int, path string, isDir bool) string {
	p := cstr(path)
	defer C.free(unsafe.Pointer(p))
	d := C.int(0)
	if isDir {
		d = 1
	}
	r := GocryptfsResolveCipherPath(C.int(id), p, d)
	if r == nil {
		return ""
	}
	defer C.free(unsafe.Pointer(r))
	return C.GoString(r)
}

func tEncryptName(id int, cipherDir, name string) string {
	d, n := cstr(cipherDir), cstr(name)
	defer C.free(unsafe.Pointer(d))
	defer C.free(unsafe.Pointer(n))
	r := GocryptfsEncryptFileName(C.int(id), d, n)
	if r == nil {
		return ""
	}
	defer C.free(unsafe.Pointer(r))
	return C.GoString(r)
}

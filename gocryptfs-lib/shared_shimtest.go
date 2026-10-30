//go:build shimtest

package main

/*
#include <stdlib.h>
#include "shim.h"
*/
import "C"

import (
	"errors"
	"unsafe"

	"github.com/adrianm/imagethat/core/drive"
)

// Wrappers over the Drive and server exports, for shared_test.go.

func setTestDriveConfig(fn func(*drive.Config)) { testDriveConfig = fn }

func tResult(r C.DecryptResult) ([]byte, error) {
	defer GocryptfsFreeDecryptResult(r)
	if r.error != nil {
		return nil, errors.New(C.GoString(r.error))
	}
	if r.data == nil {
		return []byte{}, nil
	}
	return C.GoBytes(r.data, C.int(r.size)), nil
}

func tString(p *C.char) string {
	if p == nil {
		return ""
	}
	defer C.free(unsafe.Pointer(p))
	return C.GoString(p)
}

func tDriveNew(refresh string) int {
	a, b, c := cstr("client"), cstr("secret"), cstr(refresh)
	defer C.free(unsafe.Pointer(a))
	defer C.free(unsafe.Pointer(b))
	defer C.free(unsafe.Pointer(c))
	return int(GocryptfsDriveNew(a, b, c))
}

func tDriveFree(h int)                { GocryptfsDriveFree(C.int(h)) }
func tDriveRefreshToken(h int) string { return tString(GocryptfsDriveRefreshToken(C.int(h))) }
func tDriveSignInStart(h int) string  { return tString(GocryptfsDriveSignInStart(C.int(h))) }
func tDriveSignInWait(h int) int      { return int(GocryptfsDriveSignInWait(C.int(h))) }
func tDriveSignInCancel(h int)        { GocryptfsDriveSignInCancel(C.int(h)) }

func tDriveList(h int, folder string, media bool) ([]byte, error) {
	f := cstr(folder)
	defer C.free(unsafe.Pointer(f))
	m := C.int(0)
	if media {
		m = 1
	}
	return tResult(GocryptfsDriveList(C.int(h), f, m))
}

func tDriveFindVaultRoot(h int, folder string) ([]byte, error) {
	f := cstr(folder)
	defer C.free(unsafe.Pointer(f))
	return tResult(GocryptfsDriveFindVaultRoot(C.int(h), f))
}

func tInitDrive(h int, folder, password string) int {
	f, p := cstr(folder), cstr(password)
	defer C.free(unsafe.Pointer(f))
	defer C.free(unsafe.Pointer(p))
	return int(GocryptfsInitDrive(C.int(h), f, p))
}

func tOpenDrive(h int, folder, password, cacheDir string) int {
	f, p, d := cstr(folder), cstr(password), cstr(cacheDir)
	defer C.free(unsafe.Pointer(f))
	defer C.free(unsafe.Pointer(p))
	defer C.free(unsafe.Pointer(d))
	return int(GocryptfsOpenDrive(C.int(h), f, p, d))
}

func tCachedRanges(id int, path string) ([]byte, error) {
	p := cstr(path)
	defer C.free(unsafe.Pointer(p))
	return tResult(GocryptfsCachedRanges(C.int(id), p))
}

func tDriveCacheSize(dir string) int64 {
	d := cstr(dir)
	defer C.free(unsafe.Pointer(d))
	return int64(GocryptfsDriveCacheSize(d))
}

func tServerNew(base string) int {
	b, p := cstr(base), cstr("")
	defer C.free(unsafe.Pointer(b))
	defer C.free(unsafe.Pointer(p))
	return int(GocryptfsServerNew(b, p))
}

func tServerVaults(h int) ([]byte, error) { return tResult(GocryptfsServerVaults(C.int(h))) }

func tServerUnlock(h int, id, password string) int {
	i, p := cstr(id), cstr(password)
	defer C.free(unsafe.Pointer(i))
	defer C.free(unsafe.Pointer(p))
	return int(GocryptfsServerUnlock(C.int(h), i, p))
}

func tServerList(h int, id, dir string) ([]byte, error) {
	i, d := cstr(id), cstr(dir)
	defer C.free(unsafe.Pointer(i))
	defer C.free(unsafe.Pointer(d))
	return tResult(GocryptfsServerList(C.int(h), i, d))
}

func tServerFileSize(h int, id, path string) int64 {
	i, p := cstr(id), cstr(path)
	defer C.free(unsafe.Pointer(i))
	defer C.free(unsafe.Pointer(p))
	return int64(GocryptfsServerFileSize(C.int(h), i, p))
}

func tServerReadRange(h int, id, path string, off int64, n int32) ([]byte, int32) {
	i, p := cstr(id), cstr(path)
	defer C.free(unsafe.Pointer(i))
	defer C.free(unsafe.Pointer(p))
	buf := make([]byte, n)
	got := GocryptfsServerReadRange(C.int(h), i, p, C.int64_t(off), C.int32_t(n), unsafe.Pointer(&buf[0]))
	if got < 0 {
		return nil, int32(got)
	}
	return buf[:got], int32(got)
}

func tServerUploadBegin(h int, id, dir, name string, size int64) int {
	i, d, n := cstr(id), cstr(dir), cstr(name)
	defer C.free(unsafe.Pointer(i))
	defer C.free(unsafe.Pointer(d))
	defer C.free(unsafe.Pointer(n))
	return int(GocryptfsServerUploadBegin(C.int(h), i, d, n, C.int64_t(size)))
}

func tServerMove(h int, id, from, to string) int {
	i, f, t := cstr(id), cstr(from), cstr(to)
	defer C.free(unsafe.Pointer(i))
	defer C.free(unsafe.Pointer(f))
	defer C.free(unsafe.Pointer(t))
	return int(GocryptfsServerMove(C.int(h), i, f, t))
}

func tServerDelete(h int, id, path string) int {
	i, p := cstr(id), cstr(path)
	defer C.free(unsafe.Pointer(i))
	defer C.free(unsafe.Pointer(p))
	return int(GocryptfsServerDelete(C.int(h), i, p))
}
func tServerFree(h int) { GocryptfsServerFree(C.int(h)) }

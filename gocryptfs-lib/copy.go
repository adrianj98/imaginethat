package main

/*
#include "shim.h"
*/
import "C"

import (
	"context"
	"errors"
	"sync"

	"github.com/adrianm/imagethat/core/cache"
	"github.com/adrianm/imagethat/core/remote"
	"github.com/adrianm/imagethat/core/xfer"
)

// --- Fault-tolerant copies and file caching ------------------------------------
//
// Copies into a vault and cache fetches run through core/xfer: reads that fail
// with a transient error are retried with backoff and resume where they
// stopped, and the result is committed only when complete. Each runs under a
// job, which the host creates first so that another thread can poll progress
// or cancel while the blocking call runs.

// rcCancelled is returned when a job was cancelled.
const rcCancelled = -7

type job struct {
	ctx      context.Context
	cancel   context.CancelFunc
	progress xfer.Progress
}

var (
	jobs      = make(map[int]*job)
	jobsMu    sync.Mutex
	nextJobID = 1
)

func getJob(id C.int) *job {
	jobsMu.Lock()
	defer jobsMu.Unlock()
	return jobs[int(id)]
}

//export GocryptfsJobNew
func GocryptfsJobNew() C.int {
	ctx, cancel := context.WithCancel(context.Background())
	jobsMu.Lock()
	defer jobsMu.Unlock()
	id := nextJobID
	nextJobID++
	jobs[id] = &job{ctx: ctx, cancel: cancel}
	return C.int(id)
}

// GocryptfsJobProgress reports the current file's bytes done and total and
// the retries so far. Returns 0, or -1 for an unknown job.
//
//export GocryptfsJobProgress
func GocryptfsJobProgress(jobID C.int, done, total, retries *C.int64_t) C.int {
	j := getJob(jobID)
	if j == nil {
		return rcNoConfig
	}
	d, t, r := j.progress.Snapshot()
	*done, *total, *retries = C.int64_t(d), C.int64_t(t), C.int64_t(r)
	return rcOK
}

//export GocryptfsJobCancel
func GocryptfsJobCancel(jobID C.int) {
	if j := getJob(jobID); j != nil {
		j.cancel()
	}
}

//export GocryptfsJobFree
func GocryptfsJobFree(jobID C.int) {
	jobsMu.Lock()
	j := jobs[int(jobID)]
	delete(jobs, int(jobID))
	jobsMu.Unlock()
	if j != nil {
		j.cancel()
	}
}

func (j *job) options() xfer.Options {
	j.progress.Reset()
	return xfer.Options{Progress: &j.progress}
}

func copyCode(err error) C.int {
	if errors.Is(err, context.Canceled) {
		return rcCancelled
	}
	return writeCode(err)
}

// GocryptfsCopyFileIn copies a plaintext file from the local filesystem into
// a vault at destPath. Blocks until done.
//
//export GocryptfsCopyFileIn
func GocryptfsCopyFileIn(jobID C.int, destSession C.int, destPath *C.char, srcFilePath *C.char) C.int {
	j, sess := getJob(jobID), getSession(destSession)
	if j == nil || sess == nil {
		return rcNoConfig
	}
	p := C.GoString(destPath)
	_, err := sess.s.CopyIn(j.ctx, p, xfer.OpenFile(C.GoString(srcFilePath)), j.options())
	sess.dropFile(p)
	sess.invalidateThumb(p)
	return copyCode(err)
}

// GocryptfsCopyVaultIn copies a file from one open vault (srcSession, which
// may be remote or Drive-backed) into another at destPath. Blocks until done.
//
//export GocryptfsCopyVaultIn
func GocryptfsCopyVaultIn(jobID C.int, destSession C.int, destPath *C.char, srcSession C.int, srcPath *C.char) C.int {
	j, dst, src := getJob(jobID), getSession(destSession), getSession(srcSession)
	if j == nil || dst == nil || src == nil {
		return rcNoConfig
	}
	p := C.GoString(destPath)
	_, err := dst.s.CopyIn(j.ctx, p, src.s.Opener(C.GoString(srcPath)), j.options())
	dst.dropFile(p)
	dst.invalidateThumb(p)
	return copyCode(err)
}

// GocryptfsOpenRemoteCached is GocryptfsOpenRemote with a file cache in
// cacheDir: files passed to GocryptfsCacheFile are kept there as ciphertext
// and read from disk afterwards.
//
//export GocryptfsOpenRemoteCached
func GocryptfsOpenRemoteCached(baseURL, vaultID, password, pinnedSHA256, cacheDir *C.char) C.int {
	client, err := remote.New(C.GoString(baseURL), remote.Options{PinnedSHA256: C.GoString(pinnedSHA256)})
	if err != nil {
		return rcCrypto
	}
	s, err := client.OpenVaultCached(context.Background(), C.GoString(vaultID), C.GoString(password), C.GoString(cacheDir))
	if err != nil {
		return remoteOpenCode(err)
	}
	return register(s)
}

// GocryptfsCacheFile makes a file readable without the network: a remote
// session opened with a cache fetches its ciphertext to disk; any other
// session reads the ciphertext through once, filling the host's own caches
// (Drive). Blocks until done.
//
//export GocryptfsCacheFile
func GocryptfsCacheFile(jobID C.int, sessionID C.int, relativePath *C.char) C.int {
	j, sess := getJob(jobID), getSession(sessionID)
	if j == nil || sess == nil {
		return rcNoConfig
	}
	p := C.GoString(relativePath)
	err := sess.s.CacheFile(j.ctx, p, j.options())
	// A handle opened before the fetch still reads from the network.
	sess.dropFile(p)
	return copyCode(err)
}

// GocryptfsIsCached returns 1 when a remote session's cache holds the file.
//
//export GocryptfsIsCached
func GocryptfsIsCached(sessionID C.int, relativePath *C.char) C.int {
	sess := getSession(sessionID)
	if sess == nil || !sess.s.IsCached(C.GoString(relativePath)) {
		return 0
	}
	return 1
}

//export GocryptfsUncache
func GocryptfsUncache(sessionID C.int, relativePath *C.char) C.int {
	sess := getSession(sessionID)
	if sess == nil {
		return rcNoConfig
	}
	p := C.GoString(relativePath)
	sess.dropFile(p)
	return writeCode(sess.s.Uncache(p))
}

//export GocryptfsFileCacheClear
func GocryptfsFileCacheClear(dir *C.char) C.int {
	if err := cache.ClearFiles(C.GoString(dir)); err != nil {
		return rcBackend
	}
	return rcOK
}

//export GocryptfsFileCacheSize
func GocryptfsFileCacheSize(dir *C.char) C.int64_t {
	return C.int64_t(cache.FilesSize(C.GoString(dir)))
}

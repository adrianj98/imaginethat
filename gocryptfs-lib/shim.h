/* Types shared by main.go and the shimtest helpers. cgo gives every file its
   own preamble, so the typedefs live here and both files include this header;
   identical typedef names then resolve to the same Go types. */
#ifndef GOCRYPTFS_SHIM_H
#define GOCRYPTFS_SHIM_H
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

// VFS callbacks — the host provides these for vaults whose ciphertext has no
// local path (Google Drive). All paths are vault-relative cipher paths.

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

#endif

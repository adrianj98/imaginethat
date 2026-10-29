#ifndef LIBGOCRYPTFS_H
#define LIBGOCRYPTFS_H

#include <stddef.h>
#include <stdint.h>
#include <stdlib.h>

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

// VFS callback types — Swift implements these for Drive-backed vaults.
// All paths are vault-relative cipher paths.
typedef int64_t (*VFSReadAtFn)(void* ctx, const char* path, int64_t offset, int64_t length,
                               uint8_t* buf, int64_t bufSize);
typedef int64_t (*VFSFileSizeFn)(void* ctx, const char* path);
typedef ListResult (*VFSListDirFn)(void* ctx, const char* path);

extern int GocryptfsOpen(char* vaultPath, char* password);
// Open a vault from in-memory config bytes with VFS callbacks (for Drive-backed vaults).
extern int GocryptfsOpenVFS(char* configData, int configDataLen, char* password,
                             VFSReadAtFn readAtFn, VFSFileSizeFn fileSizeFn, VFSListDirFn listDirFn,
                             void* ctx);
extern void GocryptfsClose(int sessionID);
extern ListResult GocryptfsListDir(int sessionID, char* relativePath);
extern DecryptResult GocryptfsDecryptFile(int sessionID, char* relativePath);
extern int32_t GocryptfsReadRange(int sessionID, char* relativePath, int64_t offset, int32_t length, void* outBuf);
extern int64_t GocryptfsFileSize(int sessionID, char* relativePath);
// Encrypt a filename for the given cipher directory path (needed to upload to Drive with correct name).
// Caller must free the returned string.
extern char* GocryptfsEncryptFileName(int sessionID, char* cipherDirPath, char* plainName);
extern void GocryptfsFreeDecryptResult(DecryptResult result);
extern void GocryptfsFreeListResult(ListResult result);
extern DecryptResult GocryptfsEncryptData(int sessionID, void* plaintext, int plaintextLen);
extern DecryptResult GocryptfsDecryptData(int sessionID, void* ciphertext, int ciphertextLen);
extern int GocryptfsRenameFile(int sessionID, char* sourcePath, char* destDirPath);
extern int GocryptfsMkdir(int sessionID, char* dirPath);
extern int GocryptfsDeleteFile(int sessionID, char* filePath);
extern int GocryptfsDeleteDir(int sessionID, char* dirPath);
// Replace (or create) a file from an in-memory buffer. For large imports use the
// streaming writer below so the plaintext is never held whole.
extern int GocryptfsWriteFile(int sessionID, char* relativePath, void* data, int dataLen);
// Streaming writer: CreateFile returns a writer id (> 0) or an error code (< 0).
// Write chunks with WriterWrite, then WriterClose commits the file (the entry
// appears only then) or WriterAbort discards it leaving nothing behind. Both
// end the writer; its id must not be used again. Return codes: 0 ok, -1 invalid
// session/writer or read-only vault, -2 not found, -3 invalid path, -4 already
// exists, -5 backend error.
extern int GocryptfsCreateFile(int sessionID, char* relativePath);
extern int GocryptfsWriterWrite(int writerID, void* data, int dataLen);
extern int GocryptfsWriterClose(int writerID);
extern void GocryptfsWriterAbort(int writerID);
// Resolve a plaintext vault path to its cipher equivalent. isDir=1 for directories.
// Caller must free the returned string.
extern char* GocryptfsResolveCipherPath(int sessionID, char* plainRelPath, int isDir);

// Thumbnails. The engine keeps an encrypted in-memory cache per session and,
// when diskDir is non-empty, a persistent cache sealed with the vault's own key
// (unreadable without the password). sourceID is a stable name for the vault
// (its path or gdrive:// URL); memBytes/diskBytes of 0 pick the defaults.
// Without Configure a session gets a memory-only cache on first use.
extern int GocryptfsThumbConfigure(int sessionID, char* sourceID, char* diskDir, int64_t memBytes, int64_t diskBytes);
// A JPEG preview with longest edge <= maxEdge, served from the caches or rendered
// by the engine (JPEG, PNG, GIF, WebP, BMP). size 0 with no error means the engine
// has no decoder for the file (HEIC, RAW, video): render it on the host and hand
// it back with GocryptfsThumbPut so the caches learn it. Free with
// GocryptfsFreeDecryptResult.
extern DecryptResult GocryptfsThumbGet(int sessionID, char* relativePath, int maxEdge);
extern int GocryptfsThumbPut(int sessionID, char* relativePath, int maxEdge, void* jpeg, int jpegLen);
// Drop every cached preview of a path (after an edit, move or delete).
extern void GocryptfsThumbInvalidate(int sessionID, char* relativePath);
// The persistent cache as a whole, independent of any session.
extern int GocryptfsThumbCacheClear(char* diskDir);
extern int64_t GocryptfsThumbCacheSize(char* diskDir);

#endif

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
// Open a vault on an imagethat-web server in ciphertext mode: the server sends
// the vault's encrypted files and this library decrypts them. pinnedSHA256 is
// the hex SHA-256 of a self-signed server's certificate, or "". Read-only;
// writes go through the server's decrypted API. accessPassword is the server
// access password for a client-decrypt server (the vault password then never
// leaves this process), or "" for a server-decrypt server. Returns a session
// id, or -1 no such vault, -2 wrong password, -3 network/server error, -6
// certificate mismatch, -7 wrong access password.
extern int GocryptfsOpenRemote(char* baseURL, char* vaultID, char* password, char* pinnedSHA256, char* accessPassword);
// GocryptfsServerMode: 0 server-decrypt, 1 client-decrypt, -3 network/server
// error, -6 certificate mismatch.
extern int GocryptfsServerMode(char* baseURL, char* pinnedSHA256);
extern void GocryptfsClose(int sessionID);
extern ListResult GocryptfsListDir(int sessionID, char* relativePath);
extern DecryptResult GocryptfsDecryptFile(int sessionID, char* relativePath);
extern int32_t GocryptfsReadRange(int sessionID, char* relativePath, int64_t offset, int32_t length, void* outBuf);
extern int64_t GocryptfsFileSize(int sessionID, char* relativePath);
// Encrypt a filename for the given cipher directory path (needed to upload to Drive with correct name).
// Caller must free the returned string.
extern char* GocryptfsEncryptFileName(int sessionID, char* cipherDirPath, char* plainName);
// Content of the "<hashed name>.name" sidecar for a long name, "" when the name
// is short enough to need none, NULL on error. Caller must free.
extern char* GocryptfsLongNameSidecar(int sessionID, char* cipherDirPath, char* plainName);
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

// Fault-tolerant copies and file caching. Make a job first, run the blocking
// call on a background thread, and poll JobProgress / call JobCancel from
// another. Reads that fail transiently are retried with backoff and resume
// where they stopped; the destination appears only once complete. The calls
// return 0, the write codes above, or -7 when cancelled. Free the job after.
extern int GocryptfsJobNew(void);
extern int GocryptfsJobProgress(int jobID, int64_t* done, int64_t* total, int64_t* retries);
extern void GocryptfsJobCancel(int jobID);
extern void GocryptfsJobFree(int jobID);
// Copy a plaintext file on disk into a vault.
extern int GocryptfsCopyFileIn(int jobID, int destSession, char* destPath, char* srcFilePath);
// Copy a file from one open vault (local, Drive or remote) into another.
extern int GocryptfsCopyVaultIn(int jobID, int destSession, char* destPath, int srcSession, char* srcPath);
// Copy a file from a vault on a server-decrypt server (GocryptfsServerNew
// handle, vault unlocked) into another vault.
extern int GocryptfsCopyServerIn(int jobID, int destSession, char* destPath, int server, char* vaultID, char* srcPath);
// GocryptfsOpenRemote with a ciphertext file cache in cacheDir.
extern int GocryptfsOpenRemoteCached(char* baseURL, char* vaultID, char* password, char* pinnedSHA256, char* accessPassword, char* cacheDir);
// Keep a file available locally: a cached remote session stores its ciphertext
// in the cache dir; other sessions read it through once to fill the host's
// caches (Drive).
extern int GocryptfsCacheFile(int jobID, int sessionID, char* relativePath);
// 1 when a cached remote session holds the file on disk, else 0.
extern int GocryptfsIsCached(int sessionID, char* relativePath);
extern int GocryptfsUncache(int sessionID, char* relativePath);
// The file cache directory as a whole, independent of any session.
extern int GocryptfsFileCacheClear(char* dir);
extern int64_t GocryptfsFileCacheSize(char* dir);

// Google Drive and imagethat-web servers: the same Go code the Android app
// binds. A Drive account or a server connection is a handle; vaults opened
// through them are ordinary sessions. Calls returning DecryptResult carry JSON
// (free with GocryptfsFreeDecryptResult); its error string starts with a
// stable code shared with Android: "not_found:", "exists:", "locked:",
// "wrong_password:", "not_connected:", "auth:", "cancelled:", "forbidden:" ...
// int results: 0 ok, -1 not found (or bad handle), -2 wrong password, -4
// already exists, -5 network or other failure, -6 certificate mismatch, -7
// cancelled (wrong access password for ServerLogin), -8 locked on the server,
// -9 Drive not signed in or authorization refused, -10 server refused the change.

// Drive account for the user's OAuth client; refreshToken "" until signed in.
extern int GocryptfsDriveNew(char* clientID, char* clientSecret, char* refreshToken);
extern void GocryptfsDriveFree(int drive);
// The refresh token to store after sign-in ("" when signed out). Caller frees.
extern char* GocryptfsDriveRefreshToken(int drive);
extern void GocryptfsDriveSignOut(int drive);
// Sign-in: Start returns Google's consent URL to open in the browser (caller
// frees; NULL on failure), Wait blocks on a background thread until the
// browser comes back through the engine's loopback listener, Cancel ends Wait.
extern char* GocryptfsDriveSignInStart(int drive);
extern int GocryptfsDriveSignInWait(int drive);
extern void GocryptfsDriveSignInCancel(int drive);
// A folder's entries ("root" = My Drive) as JSON
// [{id,name,mimeType,size,modifiedTime,thumbnailLink,isFolder}]. mediaOnly=1
// keeps folders, images and videos, folders first.
extern DecryptResult GocryptfsDriveList(int drive, char* folderID, int mediaOnly);
// {rootId, path} of the vault folderID is in; "not_found:" when none.
extern DecryptResult GocryptfsDriveFindVaultRoot(int drive, char* folderID);
// Plain (unencrypted) files.
extern DecryptResult GocryptfsDriveReadRange(int drive, char* fileID, int64_t offset, int64_t length);
extern DecryptResult GocryptfsDriveThumbnail(int drive, char* thumbnailLink, int maxEdge);
// Create a file in parentID, or with updateFileID set replace that file's
// content. Returns the file as JSON.
extern DecryptResult GocryptfsDriveUpload(int drive, char* parentID, char* name, char* mimeType, char* updateFileID, void* data, int64_t dataLen);
extern DecryptResult GocryptfsDriveCreateFolder(int drive, char* parentID, char* name);
extern int GocryptfsDriveDelete(int drive, char* fileID);
// Move from oldParent to newParent ("" stays) and/or rename ("" keeps the name).
extern int GocryptfsDriveMove(int drive, char* fileID, char* oldParent, char* newParent, char* newName);
// Unlock the vault in Drive folder rootID. Downloaded ciphertext is cached in
// cacheDir ("" none), shared by all Drive vaults. Returns a writable session.
extern int GocryptfsOpenDrive(int drive, char* rootID, char* password, char* cacheDir);
// Create a new empty vault in an empty Drive folder.
extern int GocryptfsInitDrive(int drive, char* folderID, char* password);
// The Drive cache as a whole. Files kept with GocryptfsCacheFile are never evicted.
extern int GocryptfsDriveCacheLimit(char* dir, int64_t limit);
extern int64_t GocryptfsDriveCacheSize(char* dir);
extern int GocryptfsDriveCacheClear(char* dir);
// Parts of a Drive vault file held on disk, JSON [[start,end],...] as
// fractions of its length ("[]" for other sessions).
extern DecryptResult GocryptfsCachedRanges(int sessionID, char* relativePath);
// Re-fetch a Drive vault directory's listing next time (changes made elsewhere).
extern int GocryptfsRefreshDir(int sessionID, char* relativePath);

// imagethat-web server connection (pinnedSHA256 as for GocryptfsOpenRemote).
// Its cookie and the passwords it re-authenticates with stay in memory.
extern int GocryptfsServerNew(char* baseURL, char* pinnedSHA256);
extern void GocryptfsServerFree(int server);
// 0 server-decrypt, 1 client-decrypt, or a negative code.
extern int GocryptfsServerGetMode(int server);
extern int GocryptfsServerLogin(int server, char* accessPassword);
// JSON [{id,name,unlocked}].
extern DecryptResult GocryptfsServerVaults(int server);
extern int GocryptfsServerUnlock(int server, char* vaultID, char* password);
extern int GocryptfsServerLock(int server, char* vaultID);
extern int GocryptfsServerLogout(int server);
// Server-decrypt API. List returns JSON [{name,isDir,size,mediaType}].
extern DecryptResult GocryptfsServerList(int server, char* vaultID, char* dir);
extern int64_t GocryptfsServerFileSize(int server, char* vaultID, char* path);
// Bytes read into outBuf, fewer at the end of the file, or a negative code.
extern int32_t GocryptfsServerReadRange(int server, char* vaultID, char* path, int64_t offset, int32_t length, void* outBuf);
// JPEG preview; size 0 when the server has none.
extern DecryptResult GocryptfsServerThumbnail(int server, char* vaultID, char* path, int size);
// Stream an upload: returns a writer ID for GocryptfsWriterWrite/Close/Abort.
// size is the byte count or -1.
extern int GocryptfsServerUploadBegin(int server, char* vaultID, char* dir, char* name, int64_t size);
extern int GocryptfsServerMkdir(int server, char* vaultID, char* path);
extern int GocryptfsServerMove(int server, char* vaultID, char* from, char* to);
extern int GocryptfsServerDelete(int server, char* vaultID, char* path);
// Open a vault over the server's ciphertext endpoints with this connection
// (after ServerLogin on a client-decrypt server), decrypting here. cacheDir ""
// for none. Returns a read-only session.
extern int GocryptfsServerOpenVault(int server, char* vaultID, char* password, char* cacheDir);

#endif

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

extern int GocryptfsOpen(char* vaultPath, char* password);
extern void GocryptfsClose(int sessionID);
extern ListResult GocryptfsListDir(int sessionID, char* relativePath);
extern DecryptResult GocryptfsDecryptFile(int sessionID, char* relativePath);
extern int32_t GocryptfsReadRange(int sessionID, char* relativePath, int64_t offset, int32_t length, void* outBuf);
extern int64_t GocryptfsFileSize(int sessionID, char* relativePath);
extern void GocryptfsFreeDecryptResult(DecryptResult result);
extern void GocryptfsFreeListResult(ListResult result);
extern DecryptResult GocryptfsEncryptData(int sessionID, void* plaintext, int plaintextLen);
extern DecryptResult GocryptfsDecryptData(int sessionID, void* ciphertext, int ciphertextLen);
extern int GocryptfsRenameFile(int sessionID, char* sourcePath, char* destDirPath);
extern int GocryptfsMkdir(int sessionID, char* dirPath);
extern int GocryptfsDeleteFile(int sessionID, char* filePath);
extern int GocryptfsDeleteDir(int sessionID, char* dirPath);
extern int GocryptfsWriteFile(int sessionID, char* relativePath, void* data, int dataLen);

#endif

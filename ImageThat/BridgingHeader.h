#ifndef BridgingHeader_h
#define BridgingHeader_h

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

#endif

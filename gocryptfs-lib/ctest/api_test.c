/*
 * End-to-end test of libgocryptfs through its public C header, the way the
 * macOS app uses it. It links the shared library built from gocryptfs-lib (a
 * cgo shim over core/vault), so it catches a header that drifts from the
 * exports, a broken return-code contract, or a memory-ownership mistake that
 * the Go-side tests cannot see.
 *
 * Usage: api_test <vault dir> <password> <plain dir>
 * The vault must be a writable copy of core/vault/testdata/vault and the plain
 * dir is core/vault/testdata/plain/full, holding the fixture's known plaintext
 * (see run.sh).
 * Exits 0 when every check passes.
 */
#include <dirent.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <unistd.h>

#include "libgocryptfs.h"

static int failures = 0;
static int checks = 0;

#define CHECK(cond, ...)                                             \
    do {                                                             \
        checks++;                                                    \
        if (cond) {                                                  \
            printf("  PASS: " __VA_ARGS__);                          \
            printf("\n");                                            \
        } else {                                                     \
            failures++;                                              \
            printf("  FAIL: " __VA_ARGS__);                          \
            printf("\n");                                            \
        }                                                            \
    } while (0)

/* Deterministic filler for the data this test writes itself. */
static void pattern_bytes(uint64_t seed, size_t n, uint8_t *out) {
    uint64_t x = seed * 0x9E3779B97F4A7C15ULL + 1;
    for (size_t i = 0; i < n; i++) {
        x ^= x << 13;
        x ^= x >> 7;
        x ^= x << 17;
        out[i] = (uint8_t)(x & 0xff);
    }
}

static int has_entry(ListResult lr, const char *name, int want_dir, int64_t *size) {
    for (int i = 0; i < lr.count; i++) {
        if (strcmp(lr.entries[i].name, name) == 0 && lr.entries[i].is_dir == want_dir) {
            if (size) *size = lr.entries[i].size;
            return 1;
        }
    }
    return 0;
}

/* Reads a whole plaintext file through ReadRange in odd-sized chunks. */
static uint8_t *read_all_ranges(int sid, const char *path, int64_t size, int32_t chunk) {
    uint8_t *out = malloc(size > 0 ? size : 1);
    int64_t off = 0;
    while (off < size) {
        int32_t want = (int32_t)((size - off) < chunk ? (size - off) : chunk);
        int32_t got = GocryptfsReadRange(sid, (char *)path, off, want, out + off);
        if (got < 0) {
            free(out);
            return NULL;
        }
        if (got == 0) break;
        off += got;
    }
    if (off != size) {
        free(out);
        return NULL;
    }
    return out;
}

/* Returns 1 if any regular file under dir contains needle. */
static int dir_contains(const char *dir, const uint8_t *needle, size_t n) {
    DIR *d = opendir(dir);
    if (!d) return 0;
    struct dirent *e;
    int found = 0;
    while (!found && (e = readdir(d)) != NULL) {
        if (strcmp(e->d_name, ".") == 0 || strcmp(e->d_name, "..") == 0) continue;
        char path[4096];
        snprintf(path, sizeof path, "%s/%s", dir, e->d_name);
        struct stat st;
        if (stat(path, &st) != 0) continue;
        if (S_ISDIR(st.st_mode)) {
            found = dir_contains(path, needle, n);
            continue;
        }
        FILE *f = fopen(path, "rb");
        if (!f) continue;
        uint8_t *buf = malloc(st.st_size + 1);
        size_t len = fread(buf, 1, st.st_size, f);
        fclose(f);
        if (len >= n && memmem(buf, len, needle, n) != NULL) found = 1;
        free(buf);
    }
    closedir(d);
    return found;
}

/* Reads dir/name whole; returns its length and sets *out (caller frees). */
static size_t read_file(const char *dir, const char *name, uint8_t **out) {
    char path[4096];
    snprintf(path, sizeof path, "%s/%s", dir, name);
    FILE *f = fopen(path, "rb");
    if (!f) return 0;
    struct stat st;
    if (stat(path, &st) != 0) { fclose(f); return 0; }
    *out = malloc(st.st_size + 1);
    size_t n = fread(*out, 1, st.st_size, f);
    fclose(f);
    return n;
}

static int count_entries(const char *dir) {
    DIR *d = opendir(dir);
    if (!d) return 0;
    struct dirent *e;
    int n = 0;
    while ((e = readdir(d)) != NULL) {
        if (strcmp(e->d_name, ".") == 0 || strcmp(e->d_name, "..") == 0) continue;
        n++;
        char path[4096];
        snprintf(path, sizeof path, "%s/%s", dir, e->d_name);
        struct stat st;
        if (stat(path, &st) == 0 && S_ISDIR(st.st_mode)) n += count_entries(path);
    }
    closedir(d);
    return n;
}

int main(int argc, char **argv) {
    if (argc != 4) {
        fprintf(stderr, "usage: %s <vault dir> <password> <plain dir>\n", argv[0]);
        return 2;
    }
    const char *vault = argv[1];
    const char *password = argv[2];
    const char *plain = argv[3];

    printf("--- open ---\n");
    CHECK(GocryptfsOpen((char *)vault, "wrong-password") == -2, "wrong password returns -2");
    CHECK(GocryptfsOpen("/nonexistent-vault-dir", (char *)password) == -1, "missing vault returns -1");
    int sid = GocryptfsOpen((char *)vault, (char *)password);
    CHECK(sid > 0, "open returns a session id (%d)", sid);
    if (sid <= 0) return 1;

    printf("--- list and read ---\n");
    ListResult root = GocryptfsListDir(sid, "");
    CHECK(root.error == NULL, "list root has no error (%s)", root.error ? root.error : "-");
    int bookkeeping = 0;
    for (int i = 0; i < root.count; i++)
        if (strncmp(root.entries[i].name, "gocryptfs.", 10) == 0) bookkeeping++;
    CHECK(bookkeeping == 0, "no gocryptfs bookkeeping files in listing");
    CHECK(has_entry(root, "subdir", 1, NULL), "subdir listed as a directory");
    int64_t large_size = 0;
    CHECK(has_entry(root, "large.bin", 0, &large_size) && large_size == 100000,
          "large.bin listed with plaintext size %lld", (long long)large_size);
    GocryptfsFreeListResult(root);

    CHECK(GocryptfsFileSize(sid, "large.bin") == 100000, "FileSize(large.bin) == 100000");
    CHECK(GocryptfsFileSize(sid, "empty.bin") == 0, "FileSize(empty.bin) == 0");
    CHECK(GocryptfsFileSize(sid, "missing.bin") == -1, "FileSize(missing) == -1");

    /* photo.jpg's plaintext is committed next to the vault, so this checks the
       decryption against bytes written by the real gocryptfs binary. */
    uint8_t *want_photo = NULL;
    size_t want_photo_len = read_file(plain, "photo.jpg", &want_photo);
    CHECK(want_photo_len > 0, "fixture plaintext photo.jpg readable (%zu bytes)", want_photo_len);
    DecryptResult photo = GocryptfsDecryptFile(sid, "photo.jpg");
    CHECK(photo.error == NULL && photo.size == (int64_t)want_photo_len &&
              memcmp(photo.data, want_photo, want_photo_len) == 0,
          "DecryptFile(photo.jpg) matches the gocryptfs-written plaintext");
    GocryptfsFreeDecryptResult(photo);
    uint8_t *photo_ranged = read_all_ranges(sid, "photo.jpg", (int64_t)want_photo_len, 4097);
    CHECK(photo_ranged && memcmp(photo_ranged, want_photo, want_photo_len) == 0,
          "ReadRange in 4097-byte chunks reassembles photo.jpg");
    free(photo_ranged);
    free(want_photo);

    DecryptResult whole = GocryptfsDecryptFile(sid, "large.bin");
    CHECK(whole.error == NULL && whole.size == 100000, "DecryptFile(large.bin) yields 100000 bytes");
    uint8_t *ranged = read_all_ranges(sid, "large.bin", 100000, 4097);
    CHECK(ranged && whole.data && memcmp(ranged, whole.data, 100000) == 0,
          "ReadRange and DecryptFile agree on large.bin");
    free(ranged);
    GocryptfsFreeDecryptResult(whole);
    uint8_t scratch[16];
    CHECK(GocryptfsReadRange(sid, "large.bin", 100000 + 5, 16, scratch) == 0, "ReadRange past EOF returns 0");
    CHECK(GocryptfsReadRange(sid, "missing.bin", 0, 16, scratch) == -1, "ReadRange(missing) returns -1");

    DecryptResult nested = GocryptfsDecryptFile(sid, "subdir/deeper/deep.bin");
    CHECK(nested.error == NULL && nested.size == 12345, "DecryptFile through two directory levels (12345 bytes)");
    GocryptfsFreeDecryptResult(nested);

    DecryptResult empty = GocryptfsDecryptFile(sid, "empty.bin");
    CHECK(empty.error == NULL && empty.size == 0, "DecryptFile(empty.bin) is empty without error");
    GocryptfsFreeDecryptResult(empty);

    DecryptResult missing = GocryptfsDecryptFile(sid, "missing.bin");
    CHECK(missing.error != NULL, "DecryptFile(missing) reports an error: %s", missing.error ? missing.error : "");
    GocryptfsFreeDecryptResult(missing);

    printf("--- streaming writer ---\n");
    const size_t import_len = 3 * 1024 * 1024 + 123;
    uint8_t *import = malloc(import_len);
    pattern_bytes(555, import_len, import);

    CHECK(GocryptfsMkdir(sid, "imported") == 0, "Mkdir(imported)");
    CHECK(GocryptfsMkdir(sid, "imported") == -4, "Mkdir(existing) returns -4");
    int wid = GocryptfsCreateFile(sid, "imported/video.bin");
    CHECK(wid > 0, "CreateFile returns a writer id (%d)", wid);
    int write_ok = 1;
    for (size_t off = 0; off < import_len; off += 256 * 1024) {
        size_t n = import_len - off < 256 * 1024 ? import_len - off : 256 * 1024;
        if (GocryptfsWriterWrite(wid, import + off, (int)n) != 0) write_ok = 0;
    }
    CHECK(write_ok, "WriterWrite accepts every chunk");
    CHECK(GocryptfsWriterClose(wid) == 0, "WriterClose commits");
    CHECK(GocryptfsWriterClose(wid) == -1, "WriterClose on a finished writer returns -1");
    CHECK(GocryptfsFileSize(sid, "imported/video.bin") == (int64_t)import_len, "streamed file has the right size");
    DecryptResult back = GocryptfsDecryptFile(sid, "imported/video.bin");
    CHECK(back.error == NULL && back.size == (int64_t)import_len && memcmp(back.data, import, import_len) == 0,
          "streamed file reads back byte for byte");
    GocryptfsFreeDecryptResult(back);
    CHECK(!dir_contains(vault, import + 1000, 64), "no plaintext anywhere in the vault directory after import");

    int dup = GocryptfsCreateFile(sid, "imported/video.bin");
    GocryptfsWriterWrite(dup, import, 10);
    CHECK(GocryptfsWriterClose(dup) == -4, "Create over an existing file returns -4");

    int before = count_entries(vault);
    int aborted = GocryptfsCreateFile(sid, "imported/aborted.bin");
    GocryptfsWriterWrite(aborted, import, 70000);
    GocryptfsWriterAbort(aborted);
    CHECK(GocryptfsFileSize(sid, "imported/aborted.bin") == -1, "aborted file is not visible");
    CHECK(count_entries(vault) == before, "abort leaves no temp file behind");

    printf("--- write, replace, seal ---\n");
    uint8_t first[20000], second[7000];
    pattern_bytes(1, sizeof first, first);
    pattern_bytes(2, sizeof second, second);
    CHECK(GocryptfsWriteFile(sid, "imported/edit.jpg", first, sizeof first) == 0, "WriteFile creates");
    uint8_t head[16];
    CHECK(GocryptfsReadRange(sid, "imported/edit.jpg", 0, 16, head) == 16 && memcmp(head, first, 16) == 0,
          "ReadRange sees the first version");
    CHECK(GocryptfsWriteFile(sid, "imported/edit.jpg", second, sizeof second) == 0, "WriteFile replaces");
    CHECK(GocryptfsReadRange(sid, "imported/edit.jpg", 0, 16, head) == 16 && memcmp(head, second, 16) == 0,
          "ReadRange sees the replacement, not a stale handle");
    CHECK(GocryptfsFileSize(sid, "imported/edit.jpg") == (int64_t)sizeof second, "replaced file has the new size");
    CHECK(!dir_contains(vault, second + 100, 64), "no plaintext on disk after replace");

    DecryptResult sealed = GocryptfsEncryptData(sid, first, sizeof first);
    CHECK(sealed.error == NULL && sealed.size > (int64_t)sizeof first, "EncryptData produces ciphertext");
    CHECK(memmem(sealed.data, sealed.size, first, 32) == NULL, "sealed blob does not contain plaintext");
    DecryptResult opened = GocryptfsDecryptData(sid, sealed.data, (int)sealed.size);
    CHECK(opened.error == NULL && opened.size == (int64_t)sizeof first && memcmp(opened.data, first, sizeof first) == 0,
          "DecryptData round-trips");
    GocryptfsFreeDecryptResult(opened);
    GocryptfsFreeDecryptResult(sealed);
    DecryptResult garbage = GocryptfsDecryptData(sid, first, 100);
    CHECK(garbage.error != NULL, "DecryptData rejects garbage");
    GocryptfsFreeDecryptResult(garbage);

    printf("--- names ---\n");
    char *cdir = GocryptfsResolveCipherPath(sid, "imported", 1);
    char *cfile = GocryptfsResolveCipherPath(sid, "imported/edit.jpg", 0);
    CHECK(cdir && cfile && strstr(cfile, "edit") == NULL, "ResolveCipherPath yields ciphertext names");
    char *cname = GocryptfsEncryptFileName(sid, cdir, "edit.jpg");
    char joined[4096];
    snprintf(joined, sizeof joined, "%s/%s", cdir, cname ? cname : "");
    CHECK(cname && strcmp(joined, cfile) == 0, "EncryptFileName agrees with ResolveCipherPath");
    char full[4096];
    snprintf(full, sizeof full, "%s/%s", vault, cfile);
    struct stat st;
    CHECK(stat(full, &st) == 0, "resolved cipher path exists on disk");
    CHECK(GocryptfsEncryptFileName(sid, cdir, "a/b") == NULL, "EncryptFileName rejects a separator");
    free(cdir);
    free(cfile);
    free(cname);

    printf("--- thumbnails ---\n");
    char cache_dir[] = "/tmp/imagethat-ctest-thumbs.XXXXXX";
    CHECK(mkdtemp(cache_dir) != NULL, "temp thumbnail cache dir");
    CHECK(GocryptfsThumbConfigure(sid, (char *)vault, cache_dir, 0, 0) == 0, "ThumbConfigure with a disk tier");
    DecryptResult th = GocryptfsThumbGet(sid, "photo.jpg", 64);
    CHECK(th.error == NULL && th.size > 4 && ((uint8_t *)th.data)[0] == 0xFF && ((uint8_t *)th.data)[1] == 0xD8,
          "ThumbGet(photo.jpg) returns a JPEG (%lld bytes)", (long long)th.size);
    DecryptResult th2 = GocryptfsThumbGet(sid, "photo.jpg", 64);
    CHECK(th2.error == NULL && th2.size == th.size && memcmp(th.data, th2.data, th.size) == 0,
          "second ThumbGet matches (cache hit)");
    GocryptfsFreeDecryptResult(th2);
    DecryptResult thv = GocryptfsThumbGet(sid, "clip.mp4", 64);
    CHECK(thv.error == NULL && thv.size == 0, "ThumbGet(clip.mp4) is empty without error (host renders video)");
    GocryptfsFreeDecryptResult(thv);
    uint8_t host_jpeg[] = {0xFF, 0xD8, 0xFF, 0xE0, 7, 7, 7, 7};
    CHECK(GocryptfsThumbPut(sid, "clip.mp4", 64, host_jpeg, sizeof host_jpeg) == 0, "ThumbPut stores a host-rendered preview");
    DecryptResult thp = GocryptfsThumbGet(sid, "clip.mp4", 64);
    CHECK(thp.error == NULL && thp.size == (int64_t)sizeof host_jpeg && memcmp(thp.data, host_jpeg, sizeof host_jpeg) == 0,
          "ThumbGet returns the host-rendered preview");
    GocryptfsFreeDecryptResult(thp);
    GocryptfsThumbInvalidate(sid, "clip.mp4");
    DecryptResult thi = GocryptfsThumbGet(sid, "clip.mp4", 64);
    CHECK(thi.error == NULL && thi.size == 0, "ThumbInvalidate drops the preview");
    GocryptfsFreeDecryptResult(thi);
    DecryptResult thm = GocryptfsThumbGet(sid, "missing.jpg", 64);
    CHECK(thm.error != NULL, "ThumbGet(missing) reports an error");
    GocryptfsFreeDecryptResult(thm);
    CHECK(GocryptfsThumbCacheSize(cache_dir) > 0, "ThumbCacheSize sees the disk tier");
    CHECK(!dir_contains(cache_dir, th.data, 32), "disk tier holds no plaintext preview");
    GocryptfsFreeDecryptResult(th);
    CHECK(GocryptfsThumbCacheClear(cache_dir) == 0 && GocryptfsThumbCacheSize(cache_dir) == 0, "ThumbCacheClear empties the disk tier");
    rmdir(cache_dir);

    printf("--- move and delete ---\n");
    CHECK(GocryptfsMkdir(sid, "dest") == 0, "Mkdir(dest)");
    CHECK(GocryptfsRenameFile(sid, "imported/edit.jpg", "dest") == 0, "RenameFile into dest");
    CHECK(GocryptfsFileSize(sid, "imported/edit.jpg") == -1, "source gone after move");
    CHECK(GocryptfsFileSize(sid, "dest/edit.jpg") == (int64_t)sizeof second, "moved file readable at destination");
    CHECK(GocryptfsRenameFile(sid, "imported/nope", "dest") == -2, "RenameFile(missing) returns -2");
    CHECK(GocryptfsDeleteFile(sid, "dest/edit.jpg") == 0, "DeleteFile");
    CHECK(GocryptfsDeleteFile(sid, "dest/edit.jpg") == -2, "DeleteFile(missing) returns -2");
    CHECK(GocryptfsDeleteDir(sid, "imported") == 0, "DeleteDir removes a tree");
    ListResult after = GocryptfsListDir(sid, "");
    CHECK(after.error == NULL && !has_entry(after, "imported", 1, NULL), "deleted directory is gone from the listing");
    GocryptfsFreeListResult(after);
    CHECK(!dir_contains(vault, import + 1000, 64), "still no plaintext on disk at the end");

    printf("--- close ---\n");
    GocryptfsClose(sid);
    ListResult closed = GocryptfsListDir(sid, "");
    CHECK(closed.error != NULL, "ListDir on a closed session reports an error");
    GocryptfsFreeListResult(closed);
    GocryptfsClose(sid); /* double close must be harmless */

    free(import);
    printf("\n%d checks, %d failed\n", checks, failures);
    return failures == 0 ? 0 : 1;
}

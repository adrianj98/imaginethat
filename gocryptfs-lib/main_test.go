//go:build shimtest

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fixture is the one core/vault's tests use: made by the real gocryptfs
// binary (see core/vault/fixture_test.go).
const (
	fixtureDir      = "../core/vault/testdata/vault"
	fixturePassword = "test-password-123"
)

// openCopy unlocks a writable copy of the fixture vault through the C API.
func openCopy(t *testing.T) (int, string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(fixtureDir, "gocryptfs.conf")); err != nil {
		t.Skipf("fixture vault missing: %v", err)
	}
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(fixtureDir)); err != nil {
		t.Fatal(err)
	}
	// gocryptfs writes bookkeeping files read-only; the copy must be writable
	// for rename-over and remove to work.
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			_ = os.Chmod(p, 0o600)
		}
		return nil
	})
	id := tOpen(dir, fixturePassword)
	if id <= 0 {
		t.Fatalf("GocryptfsOpen = %d", id)
	}
	t.Cleanup(func() { tClose(id) })
	return id, dir
}

func pattern(seed byte, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7+int(seed)) ^ byte(i>>8)
	}
	return b
}

func TestOpenCodes(t *testing.T) {
	if _, err := os.Stat(filepath.Join(fixtureDir, "gocryptfs.conf")); err != nil {
		t.Skip("fixture vault missing")
	}
	if got := tOpen(fixtureDir, "not the password"); got != rcWrongPassword {
		t.Errorf("wrong password -> %d, want %d", got, rcWrongPassword)
	}
	if got := tOpen(t.TempDir(), fixturePassword); got != rcNoConfig {
		t.Errorf("no config -> %d, want %d", got, rcNoConfig)
	}
	id := tOpen(fixtureDir, fixturePassword)
	if id <= 0 {
		t.Fatalf("open fixture = %d", id)
	}
	tClose(id)
	if _, err := tList(id, ""); err == nil {
		t.Error("ListDir on a closed session succeeded")
	}
}

func TestListAndRead(t *testing.T) {
	id, _ := openCopy(t)
	entries, err := tList(id, "")
	if err != nil {
		t.Fatal(err)
	}
	var files []tEntry
	for _, e := range entries {
		if strings.HasPrefix(e.Name, "gocryptfs.") {
			t.Errorf("bookkeeping file leaked into listing: %s", e.Name)
		}
		if !e.IsDir {
			files = append(files, e)
		}
	}
	if len(files) == 0 {
		t.Fatal("no files in fixture root")
	}
	for _, f := range files {
		whole, err := tDecryptFile(id, f.Name)
		if err != nil {
			t.Fatalf("DecryptFile(%s): %v", f.Name, err)
		}
		if int64(len(whole)) != f.Size || tFileSize(id, f.Name) != f.Size {
			t.Errorf("%s: sizes disagree: list=%d decrypt=%d fileSize=%d", f.Name, f.Size, len(whole), tFileSize(id, f.Name))
		}
		// Range reads in odd-sized chunks straddling block boundaries must
		// reassemble to the whole file.
		var got []byte
		for off := int64(0); off < f.Size; {
			chunk, n := tReadRange(id, f.Name, off, 4097)
			if n < 0 {
				t.Fatalf("ReadRange(%s, %d) failed", f.Name, off)
			}
			if n == 0 {
				break
			}
			got = append(got, chunk...)
			off += int64(n)
		}
		if !bytes.Equal(got, whole) {
			t.Errorf("%s: range reassembly mismatch (%d vs %d bytes)", f.Name, len(got), len(whole))
		}
		// Past the end: zero bytes, not an error.
		if _, n := tReadRange(id, f.Name, f.Size+10, 100); n != 0 {
			t.Errorf("%s: read past EOF returned %d", f.Name, n)
		}
	}
	if _, n := tReadRange(id, "no-such-file", 0, 10); n != -1 {
		t.Errorf("ReadRange(missing) = %d, want -1", n)
	}
	if tFileSize(id, "no-such-file") != -1 {
		t.Error("FileSize(missing) should be -1")
	}
}

func TestStreamingWriter(t *testing.T) {
	id, dir := openCopy(t)
	want := pattern(5, 3*1024*1024+123)

	wid := tCreate(id, "imported/video.bin")
	if wid <= 0 {
		t.Fatalf("CreateFile = %d", wid)
	}
	// The parent directory does not exist yet: Create must fail on Close, not
	// silently invent a directory without a diriv.
	if rc := tWrite(wid, want[:10]); rc != rcOK {
		// Writes may already observe the failure; either way Close reports it.
		t.Logf("early write rc=%d", rc)
	}
	if rc := tWriterClose(wid); rc == rcOK {
		t.Fatal("Create into a missing directory succeeded")
	}

	if rc := tMkdir(id, "imported"); rc != rcOK {
		t.Fatalf("Mkdir = %d", rc)
	}
	wid = tCreate(id, "imported/video.bin")
	if wid <= 0 {
		t.Fatalf("CreateFile = %d", wid)
	}
	for off := 0; off < len(want); off += 256 * 1024 {
		end := min(off+256*1024, len(want))
		if rc := tWrite(wid, want[off:end]); rc != rcOK {
			t.Fatalf("WriterWrite at %d = %d", off, rc)
		}
	}
	if rc := tWriterClose(wid); rc != rcOK {
		t.Fatalf("WriterClose = %d", rc)
	}
	got, err := tDecryptFile(id, "imported/video.bin")
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("streamed file does not read back: err=%v len=%d", err, len(got))
	}

	// No plaintext anywhere in the vault directory.
	assertNoPlaintext(t, dir, want[1000:1064])

	// Creating over an existing file is refused.
	wid = tCreate(id, "imported/video.bin")
	tWrite(wid, []byte("x"))
	if rc := tWriterClose(wid); rc != rcExists {
		t.Errorf("Create over existing = %d, want %d", rc, rcExists)
	}

	// Abort leaves nothing behind.
	before := countEntries(t, dir)
	wid = tCreate(id, "imported/aborted.bin")
	tWrite(wid, want[:70000])
	tWriterAbort(wid)
	if tFileSize(id, "imported/aborted.bin") != -1 {
		t.Error("aborted file is visible")
	}
	if after := countEntries(t, dir); after != before {
		t.Errorf("abort left %d extra entries in the vault dir", after-before)
	}

	// Writer ids are single use.
	if rc := tWriterClose(wid); rc != rcNoConfig {
		t.Errorf("Close of a finished writer = %d, want %d", rc, rcNoConfig)
	}
}

func TestWriteFileReplaces(t *testing.T) {
	id, dir := openCopy(t)
	first, second := pattern(1, 20000), pattern(2, 7000)
	if rc := tWriteFile(id, "edit.jpg", first); rc != rcOK {
		t.Fatalf("WriteFile(create) = %d", rc)
	}
	// Prime the open-file cache, then overwrite: the next read must see the
	// new content, not a stale handle.
	if chunk, _ := tReadRange(id, "edit.jpg", 0, 16); !bytes.Equal(chunk, first[:16]) {
		t.Fatal("first read wrong")
	}
	if rc := tWriteFile(id, "edit.jpg", second); rc != rcOK {
		t.Fatalf("WriteFile(replace) = %d", rc)
	}
	got, _ := tDecryptFile(id, "edit.jpg")
	if !bytes.Equal(got, second) {
		t.Error("replace did not take effect")
	}
	if chunk, _ := tReadRange(id, "edit.jpg", 0, 16); !bytes.Equal(chunk, second[:16]) {
		t.Error("cached handle served stale content after replace")
	}
	assertNoPlaintext(t, dir, second[100:164])
}

func TestSealRoundTripAndNames(t *testing.T) {
	id, dir := openCopy(t)
	thumb := pattern(9, 50000)
	sealed, err := tEncryptData(id, thumb)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, thumb[:32]) {
		t.Error("sealed blob contains plaintext")
	}
	back, err := tDecryptData(id, sealed)
	if err != nil || !bytes.Equal(back, thumb) {
		t.Errorf("EncryptData/DecryptData round trip failed: %v", err)
	}
	if empty, err := tEncryptData(id, nil); err != nil || len(empty) != 0 {
		t.Errorf("EncryptData(empty) = %d bytes, %v", len(empty), err)
	}

	if rc := tMkdir(id, "names"); rc != rcOK {
		t.Fatal(rc)
	}
	if rc := tWriteFile(id, "names/photo.jpg", thumb[:100]); rc != rcOK {
		t.Fatal(rc)
	}
	cDir := tResolve(id, "names", true)
	cFile := tResolve(id, "names/photo.jpg", false)
	if cDir == "" || cFile == "" || strings.Contains(cFile, "photo") {
		t.Fatalf("resolve: dir=%q file=%q", cDir, cFile)
	}
	if _, err := os.Stat(filepath.Join(dir, cFile)); err != nil {
		t.Errorf("resolved cipher path does not exist: %v", err)
	}
	if got := filepath.Join(cDir, tEncryptName(id, cDir, "photo.jpg")); got != cFile {
		t.Errorf("EncryptFileName gives %q, ResolveCipherPath gives %q", got, cFile)
	}
	if tEncryptName(id, cDir, "a/b") != "" {
		t.Error("EncryptFileName accepted a path with a separator")
	}
}

func TestMoveAndDelete(t *testing.T) {
	id, _ := openCopy(t)
	data := pattern(3, 5000)
	tMkdir(id, "src")
	tMkdir(id, "dst")
	if rc := tWriteFile(id, "src/a.bin", data); rc != rcOK {
		t.Fatal(rc)
	}
	// Warm the handle cache so the move has something to invalidate.
	tReadRange(id, "src/a.bin", 0, 10)

	if rc := tRename(id, "src/a.bin", "dst"); rc != rcOK {
		t.Fatalf("RenameFile = %d", rc)
	}
	if tFileSize(id, "src/a.bin") != -1 {
		t.Error("source still present after move")
	}
	if got, _ := tDecryptFile(id, "dst/a.bin"); !bytes.Equal(got, data) {
		t.Error("moved file does not read back")
	}
	if rc := tRename(id, "src/missing", "dst"); rc != rcWrongPassword {
		t.Errorf("Rename(missing) = %d, want %d (not found)", rc, rcWrongPassword)
	}
	if rc := tDeleteFile(id, "dst/a.bin"); rc != rcOK {
		t.Errorf("DeleteFile = %d", rc)
	}
	if rc := tDeleteFile(id, "dst/a.bin"); rc != rcWrongPassword {
		t.Errorf("DeleteFile(missing) = %d, want %d", rc, rcWrongPassword)
	}
	tWriteFile(id, "dst/b.bin", data)
	if rc := tDeleteDir(id, "dst"); rc != rcOK {
		t.Errorf("DeleteDir = %d", rc)
	}
	if entries, _ := tList(id, ""); hasName(entries, "dst") {
		t.Error("deleted directory still listed")
	}
	if rc := tMkdir(id, "src"); rc != rcExists {
		t.Errorf("Mkdir(existing) = %d, want %d", rc, rcExists)
	}
}

func hasName(entries []tEntry, name string) bool {
	for _, e := range entries {
		if e.Name == name {
			return true
		}
	}
	return false
}

func countEntries(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	_ = filepath.WalkDir(dir, func(string, os.DirEntry, error) error { n++; return nil })
	return n
}

// assertNoPlaintext walks the vault directory and fails if any file contains
// the marker: the shim must never let decrypted bytes reach the backend.
func assertNoPlaintext(t *testing.T, dir string, marker []byte) {
	t.Helper()
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err == nil && bytes.Contains(b, marker) {
			t.Errorf("plaintext found on disk in %s", p)
		}
		return nil
	})
}

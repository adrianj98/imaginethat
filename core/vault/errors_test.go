package vault

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/adrianm/imagethat/core/internal/gocryptfs/contentenc"
)

// failingVFS wraps a VFS and fails at failAtOffset, so tests can distinguish
// "end of file" from "the disk broke". A read spanning the failure point
// returns what lies before it plus the error, as a real disk does.
type failingVFS struct {
	VFS
	failAtOffset int64
	err          error
}

func (v *failingVFS) ReadAt(path string, offset int64, buf []byte) (int, error) {
	if offset >= v.failAtOffset {
		return 0, v.err
	}
	if end := offset + int64(len(buf)); end > v.failAtOffset {
		n, _ := v.VFS.ReadAt(path, offset, buf[:v.failAtOffset-offset])
		return n, v.err
	}
	return v.VFS.ReadAt(path, offset, buf)
}

var errBackendExploded = errors.New("simulated backend failure")

// TestReadAtSurfacesBackendErrors is the regression test for a backend I/O
// error being reported as io.EOF.
//
// ReadAt used to `break` on a zero-byte read without inspecting the error, so
// a failing disk produced a silently truncated body under a 200 or 206 —
// the client would see a corrupt file and nothing would be logged.
func TestReadAtSurfacesBackendErrors(t *testing.T) {
	dir := copyVaultTree(t, fixtureDir)
	s, err := OpenLocal(dir, fixturePassword)
	if err != nil {
		t.Fatalf("OpenLocal: %v", err)
	}
	defer s.Close()

	// Fail once the read passes the first few blocks.
	s.fs = &failingVFS{VFS: s.fs, failAtOffset: 20000, err: errBackendExploded}

	fh, err := s.Open("large.bin")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer fh.Close()

	buf := make([]byte, 100000)
	_, err = fh.ReadAt(buf, 0)
	if err == nil {
		t.Fatal("ReadAt returned no error despite a failing backend")
	}
	if errors.Is(err, io.EOF) {
		t.Fatalf("backend failure reported as io.EOF: %v", err)
	}
	if !errors.Is(err, errBackendExploded) {
		t.Errorf("got %v, want it to wrap the backend error", err)
	}
}

// shortReadVFS reports the true file size but stops delivering ciphertext at
// cutoff, modelling a file that shrinks between stat and read.
//
// The cut is on a cipher-block boundary so the blocks that are returned still
// authenticate — a mid-block cut fails the GCM tag instead, which is a
// different (and already correct) path.
type shortReadVFS struct {
	VFS
	cutoff int64
}

func (v *shortReadVFS) ReadAt(path string, offset int64, buf []byte) (int, error) {
	if offset >= v.cutoff {
		return 0, io.EOF
	}
	if end := offset + int64(len(buf)); end > v.cutoff {
		n, _ := v.VFS.ReadAt(path, offset, buf[:v.cutoff-offset])
		return n, io.EOF
	}
	return v.VFS.ReadAt(path, offset, buf)
}

// TestReadFileDoesNotZeroPad is the regression test for ReadFile returning a
// full-size buffer padded with zeros when the ciphertext ran out early.
func TestReadFileDoesNotZeroPad(t *testing.T) {
	dir := copyVaultTree(t, fixtureDir)
	s, err := OpenLocal(dir, fixturePassword)
	if err != nil {
		t.Fatalf("OpenLocal: %v", err)
	}
	defer s.Close()

	const wholeBlocks = 5
	cipherBS := int64(s.contentEnc.CipherBS())
	plainBS := int64(s.contentEnc.PlainBS())
	cutoff := int64(contentenc.HeaderLen) + wholeBlocks*cipherBS
	wantLen := int(wholeBlocks * plainBS)

	s.fs = &shortReadVFS{VFS: s.fs, cutoff: cutoff}

	got, err := s.ReadFile("large.bin")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(got) != wantLen {
		t.Fatalf("ReadFile returned %d bytes, want %d — a full-size buffer means it zero-padded", len(got), wantLen)
	}
	want := patternBytes(7, 100000)
	if !bytes.Equal(got, want[:wantLen]) {
		t.Error("returned bytes do not match the plaintext prefix")
	}
}

// TestStaleDirIVRecovers covers a directory being replaced by another process
// — a second browser session, or the macOS app on the same vault.
//
// The per-session dirIV cache would otherwise keep serving the old IV, under
// which no name decrypts, and ListDir's blanket skip turned that into a folder
// that silently renders empty.
func TestStaleDirIVRecovers(t *testing.T) {
	s, dir := openWritableVault(t, fixtureDir)

	// Populate the cache for subdir.
	if _, err := s.ListDir("subdir"); err != nil {
		t.Fatalf("ListDir(subdir): %v", err)
	}

	cipherDir, err := s.resolveCipherPath("subdir")
	if err != nil {
		t.Fatalf("resolveCipherPath: %v", err)
	}

	// Simulate another process recreating the directory: new IV, new content
	// encrypted under it. A second session on the same vault is the easiest
	// way to produce genuinely valid ciphertext.
	other, err := OpenLocal(dir, fixturePassword)
	if err != nil {
		t.Fatalf("second OpenLocal: %v", err)
	}
	defer other.Close()
	if err := other.Remove("subdir"); err != nil {
		t.Fatalf("Remove(subdir): %v", err)
	}
	if err := other.Mkdir("subdir"); err != nil {
		t.Fatalf("Mkdir(subdir): %v", err)
	}
	if _, err := other.Create("subdir/fresh.bin", strings.NewReader("hello")); err != nil {
		t.Fatalf("Create: %v", err)
	}

	newCipherDir, err := other.resolveCipherPath("subdir")
	if err != nil {
		t.Fatalf("resolveCipherPath (other): %v", err)
	}
	if newCipherDir != cipherDir {
		// The directory name encrypts under the *parent's* IV, which did not
		// change, so this should be the same path. If it is not, the test is
		// not exercising what it claims to.
		t.Fatalf("cipher dir changed (%s -> %s); test assumption broken", cipherDir, newCipherDir)
	}

	// The first session still holds the old IV in its cache.
	entries, err := s.ListDir("subdir")
	if err != nil {
		t.Fatalf("ListDir after external recreate: %v", err)
	}
	var found bool
	for _, e := range entries {
		if e.Name == "fresh.bin" {
			found = true
		}
	}
	if !found {
		t.Errorf("stale dirIV cache hid the directory's new contents; got %+v", entries)
	}
}

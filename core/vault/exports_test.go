package vault

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The exports in this file exist for the host shims (gocryptfs-lib for macOS,
// mobile for Android): whole-buffer sealing for persisted thumbnails, name and
// path resolution for backends that address ciphertext by name, and Replace
// for saving an edited image over the original.

func TestSealOpenBlobRoundTrip(t *testing.T) {
	s := openFixture(t)
	for _, size := range []int{0, 1, 4095, 4096, 4097, 3*4096 + 17, 300_000} {
		want := patternBytes(uint64(size)+7, size)
		sealed, err := s.SealBlob(want)
		if err != nil {
			t.Fatalf("SealBlob(%d): %v", size, err)
		}
		if size == 0 && len(sealed) != 0 {
			t.Errorf("SealBlob(empty) = %d bytes, want 0 (gocryptfs stores an empty file as empty)", len(sealed))
		}
		if size > 0 && bytes.Contains(sealed, want[:min(size, 64)]) {
			t.Errorf("SealBlob(%d): ciphertext contains plaintext prefix", size)
		}
		got, err := s.OpenBlob(sealed)
		if err != nil {
			t.Fatalf("OpenBlob(%d): %v", size, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("round trip %d bytes: mismatch (got %d)", size, len(got))
		}
	}
}

func TestSealBlobMatchesFileFormat(t *testing.T) {
	// A sealed blob is a gocryptfs ciphertext file: writing it into the vault
	// directory under an encrypted name must read back through the normal
	// file path.
	s, dir := openWritable(t)
	want := patternBytes(42, 10_000)
	sealed, err := s.SealBlob(want)
	if err != nil {
		t.Fatal(err)
	}
	cName, err := s.EncryptName("", "sealed.bin")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, cName), sealed, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadFile("sealed.bin")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Error("blob written as a file does not read back")
	}
}

func TestOpenBlobRejectsGarbage(t *testing.T) {
	s := openFixture(t)
	if _, err := s.OpenBlob([]byte("short")); !errors.Is(err, ErrCrypto) {
		t.Errorf("OpenBlob(short) = %v, want ErrCrypto", err)
	}
	sealed, _ := s.SealBlob([]byte("hello world"))
	sealed[len(sealed)-1] ^= 0xff
	if _, err := s.OpenBlob(sealed); err == nil {
		t.Error("OpenBlob accepted a tampered block")
	}
}

func TestEncryptNameAndCipherPath(t *testing.T) {
	s := openFixture(t)
	files := fixtureFiles()
	if len(files) == 0 {
		t.Skip("no fixture files")
	}
	for _, f := range files {
		cp, err := s.CipherPath(f.Path)
		if err != nil {
			t.Errorf("CipherPath(%s): %v", f.Path, err)
			continue
		}
		if cp == "" || strings.Contains(cp, filepath.Base(f.Path)) {
			t.Errorf("CipherPath(%s) = %q: looks like plaintext", f.Path, cp)
		}
		if _, err := os.Stat(filepath.Join(fixtureDir, cp)); err != nil {
			t.Errorf("CipherPath(%s) = %q does not exist on disk: %v", f.Path, cp, err)
		}
		// EncryptName of the final component inside the resolved parent must
		// agree with the full resolution.
		parent, base := filepath.Split(f.Path)
		cParent, err := s.CipherPath(strings.TrimSuffix(parent, "/"))
		if err != nil {
			t.Fatal(err)
		}
		cName, err := s.EncryptName(cParent, base)
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Join(cParent, cName) != cp {
			t.Errorf("EncryptName(%q,%q) = %q, but CipherPath gives %q", cParent, base, cName, cp)
		}
	}
	if root, err := s.CipherPath(""); err != nil || root != "" {
		t.Errorf("CipherPath(root) = %q, %v; want \"\"", root, err)
	}
	for _, bad := range []string{"", ".", "..", "a/b", "a\\b"} {
		if _, err := s.EncryptName("", bad); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("EncryptName(%q) = %v, want ErrInvalidPath", bad, err)
		}
	}
	if _, err := s.CipherPath("../x"); !errors.Is(err, ErrInvalidPath) {
		t.Errorf("CipherPath(../x) = %v, want ErrInvalidPath", err)
	}
}

func TestReplaceOverwritesInPlace(t *testing.T) {
	s, dir := openWritable(t)
	first := patternBytes(1, 20_000)
	second := patternBytes(2, 5_000)

	if _, err := s.Replace("edited.jpg", bytes.NewReader(first)); err != nil {
		t.Fatalf("Replace (create): %v", err)
	}
	cp, _ := s.CipherPath("edited.jpg")
	before, _ := os.ReadDir(dir)

	n, err := s.Replace("edited.jpg", bytes.NewReader(second))
	if err != nil {
		t.Fatalf("Replace (overwrite): %v", err)
	}
	if n != int64(len(second)) {
		t.Errorf("Replace wrote %d, want %d", n, len(second))
	}
	got, err := s.ReadFile("edited.jpg")
	if err != nil || !bytes.Equal(got, second) {
		t.Errorf("after Replace: got %d bytes, err %v; want %d", len(got), err, len(second))
	}
	if cp2, _ := s.CipherPath("edited.jpg"); cp2 != cp {
		t.Errorf("ciphertext name changed on Replace: %q -> %q", cp, cp2)
	}
	after, _ := os.ReadDir(dir)
	if len(after) != len(before) {
		t.Errorf("Replace left %d entries in the vault dir, had %d (temp file not cleaned up?)", len(after), len(before))
	}
	for _, e := range after {
		if strings.HasPrefix(e.Name(), ".imagethat-replace-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestReplaceKeepsOldContentOnFailure(t *testing.T) {
	s, _ := openWritable(t)
	want := patternBytes(3, 9_000)
	if _, err := s.Replace("keep.bin", bytes.NewReader(want)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Replace("keep.bin", errReader{}); err == nil {
		t.Fatal("Replace with a failing reader succeeded")
	}
	got, err := s.ReadFile("keep.bin")
	if err != nil || !bytes.Equal(got, want) {
		t.Errorf("old content lost after failed Replace: %d bytes, %v", len(got), err)
	}
}

func TestReplaceOnClosedSession(t *testing.T) {
	s, _ := openWritable(t)
	s.Close()
	if _, err := s.Replace("x", bytes.NewReader(nil)); !errors.Is(err, ErrClosed) {
		t.Errorf("Replace after Close = %v, want ErrClosed", err)
	}
	if _, err := s.SealBlob([]byte("x")); !errors.Is(err, ErrClosed) {
		t.Errorf("SealBlob after Close = %v, want ErrClosed", err)
	}
}

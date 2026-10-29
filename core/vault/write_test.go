package vault

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// openWritable unlocks a private, writable copy of the main test vault, so a
// test can mutate it without disturbing testdata.
func openWritable(t *testing.T) (*Session, string) {
	t.Helper()
	return openWritableVault(t, fixtureDir)
}

// copyFixtureVault copies the main test vault into a temp dir, without
// unlocking it.
func copyFixtureVault(t *testing.T) string {
	t.Helper()
	return copyVaultTree(t, fixtureDir)
}

func TestCreateAndReadBack(t *testing.T) {
	s, _ := openWritable(t)

	cases := []struct {
		name string
		size int
	}{
		{"tiny.bin", 1},
		{"exact-block.bin", 4096},
		{"just-over.bin", 4097},
		{"multi.bin", 50000},
		{"zero.bin", 0},
	}
	for _, tc := range cases {
		want := patternBytes(999, tc.size)
		n, err := s.Create(tc.name, bytes.NewReader(want))
		if err != nil {
			t.Errorf("Create(%s): %v", tc.name, err)
			continue
		}
		if n != int64(tc.size) {
			t.Errorf("Create(%s) wrote %d bytes, want %d", tc.name, n, tc.size)
		}
		got, err := s.ReadFile(tc.name)
		if err != nil {
			t.Errorf("ReadFile(%s): %v", tc.name, err)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: round-trip mismatch (got %d bytes, want %d)", tc.name, len(got), len(want))
		}
		size, err := s.FileSize(tc.name)
		if err != nil || size != int64(tc.size) {
			t.Errorf("FileSize(%s) = %d, %v; want %d", tc.name, size, err, tc.size)
		}
	}
}

// TestCreateEmptyFileHasNoHeader pins the format detail that an empty file is
// a zero-byte ciphertext file, not an 18-byte header.
func TestCreateEmptyFileHasNoHeader(t *testing.T) {
	s, dir := openWritable(t)
	if _, err := s.Create("zero.bin", bytes.NewReader(nil)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	cipherPath, err := s.resolveCipherPath("zero.bin")
	if err != nil {
		t.Fatalf("resolveCipherPath: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, cipherPath))
	if err != nil {
		t.Fatalf("stat ciphertext: %v", err)
	}
	if info.Size() != 0 {
		t.Errorf("empty file ciphertext is %d bytes, want 0", info.Size())
	}
}

func TestCreateRefusesExisting(t *testing.T) {
	s, _ := openWritable(t)
	if _, err := s.Create("large.bin", strings.NewReader("x")); !errors.Is(err, ErrExists) {
		t.Errorf("Create over existing file: got %v, want ErrExists", err)
	}
}

func TestCreateCleansUpOnFailure(t *testing.T) {
	s, dir := openWritable(t)

	// A reader that fails partway must leave no temp file behind.
	r := io.MultiReader(bytes.NewReader(patternBytes(1, 10000)), errReader{})
	if _, err := s.Create("doomed.bin", r); err == nil {
		t.Fatal("Create should have failed")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".imagethat-upload-") {
			t.Errorf("temp file %q left behind after a failed Create", e.Name())
		}
	}
	if _, err := s.FileSize("doomed.bin"); err == nil {
		t.Error("failed Create left a visible file")
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func TestCreateLongName(t *testing.T) {
	s, _ := openWritable(t)
	name := strings.Repeat("another-very-long-name-", 8) + "x.bin"
	want := patternBytes(555, 3000)

	if _, err := s.Create(name, bytes.NewReader(want)); err != nil {
		t.Fatalf("Create(long name): %v", err)
	}
	got, err := s.ReadFile(name)
	if err != nil {
		t.Fatalf("ReadFile(long name): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Error("long-name file round-trip mismatch")
	}

	// It must appear in the listing with its plaintext name.
	entries, err := s.ListDir("")
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	var found bool
	for _, e := range entries {
		if e.Name == name {
			found = true
		}
		if strings.HasPrefix(e.Name, "gocryptfs.longname.") {
			t.Errorf("undecoded long name leaked: %q", e.Name)
		}
	}
	if !found {
		t.Error("long-name file missing from listing")
	}
}

func TestMkdir(t *testing.T) {
	s, dir := openWritable(t)

	if err := s.Mkdir("newdir"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	entries, err := s.ListDir("newdir")
	if err != nil {
		t.Fatalf("ListDir(newdir): %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("new directory has %d entries, want 0", len(entries))
	}

	// It must carry its own gocryptfs.diriv.
	cipherDir, err := s.resolveCipherPath("newdir")
	if err != nil {
		t.Fatalf("resolveCipherPath: %v", err)
	}
	iv, err := os.ReadFile(filepath.Join(dir, cipherDir, "gocryptfs.diriv"))
	if err != nil {
		t.Fatalf("read new diriv: %v", err)
	}
	if len(iv) != dirIVLen {
		t.Errorf("diriv is %d bytes, want %d", len(iv), dirIVLen)
	}

	// And a file created inside must round-trip.
	want := patternBytes(42, 9000)
	if _, err := s.Create("newdir/inner.bin", bytes.NewReader(want)); err != nil {
		t.Fatalf("Create in new dir: %v", err)
	}
	got, err := s.ReadFile("newdir/inner.bin")
	if err != nil {
		t.Fatalf("ReadFile in new dir: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Error("file in new directory round-trip mismatch")
	}

	if err := s.Mkdir("newdir"); !errors.Is(err, ErrExists) {
		t.Errorf("Mkdir over existing: got %v, want ErrExists", err)
	}
}

func TestMkdirNested(t *testing.T) {
	s, _ := openWritable(t)
	if err := s.Mkdir("subdir/child"); err != nil {
		t.Fatalf("Mkdir(subdir/child): %v", err)
	}
	if _, err := s.Create("subdir/child/f.bin", strings.NewReader("hello")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := s.ReadFile("subdir/child/f.bin")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

func TestRemoveFile(t *testing.T) {
	s, _ := openWritable(t)
	if err := s.Remove("large.bin"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := s.FileSize("large.bin"); err == nil {
		t.Error("file still readable after Remove")
	}
	if err := s.Remove("large.bin"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Remove of missing file: got %v, want ErrNotFound", err)
	}
}

// TestRemoveLongNameCleansSidecar makes sure deleting a long-named file does
// not orphan its .name file, which would otherwise accumulate forever.
func TestRemoveLongNameCleansSidecar(t *testing.T) {
	s, dir := openWritable(t)

	before := countSidecars(t, dir)
	if err := s.Remove(longFixtureName); err != nil {
		t.Fatalf("Remove(long name): %v", err)
	}
	if after := countSidecars(t, dir); after != before-1 {
		t.Errorf("sidecar count = %d, want %d", after, before-1)
	}
}

func countSidecars(t *testing.T, dir string) int {
	t.Helper()
	var n int
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(info.Name(), ".name") {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	return n
}

func TestRemoveDirectoryRecursively(t *testing.T) {
	s, _ := openWritable(t)
	if err := s.Remove("subdir"); err != nil {
		t.Fatalf("Remove(subdir): %v", err)
	}
	if _, err := s.ListDir("subdir"); err == nil {
		t.Error("directory still listable after Remove")
	}
}

func TestRemoveRootRefused(t *testing.T) {
	s, _ := openWritable(t)
	if err := s.Remove(""); !errors.Is(err, ErrInvalidPath) {
		t.Errorf("Remove(root): got %v, want ErrInvalidPath", err)
	}
}

func TestMoveRename(t *testing.T) {
	s, _ := openWritable(t)
	want, err := s.ReadFile("large.bin")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	if err := s.Move("large.bin", "renamed.bin"); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if _, err := s.FileSize("large.bin"); err == nil {
		t.Error("source still exists after Move")
	}
	got, err := s.ReadFile("renamed.bin")
	if err != nil {
		t.Fatalf("ReadFile(renamed): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Error("content changed across a rename")
	}
}

// TestMoveAcrossDirectories is the case that needs the name re-encrypted under
// the destination directory's IV.
func TestMoveAcrossDirectories(t *testing.T) {
	s, _ := openWritable(t)
	want, err := s.ReadFile("block-8192.bin")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	if err := s.Move("block-8192.bin", "subdir/deeper/moved.bin"); err != nil {
		t.Fatalf("Move: %v", err)
	}
	got, err := s.ReadFile("subdir/deeper/moved.bin")
	if err != nil {
		t.Fatalf("ReadFile(moved): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Error("content changed across a cross-directory move")
	}

	entries, err := s.ListDir("subdir/deeper")
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	var found bool
	for _, e := range entries {
		if e.Name == "moved.bin" {
			found = true
		}
	}
	if !found {
		t.Error("moved file missing from destination listing")
	}
}

func TestMoveToLongName(t *testing.T) {
	s, _ := openWritable(t)
	longDest := strings.Repeat("destination-name-part-", 9) + ".bin"
	want, err := s.ReadFile("block-4096.bin")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if err := s.Move("block-4096.bin", longDest); err != nil {
		t.Fatalf("Move to long name: %v", err)
	}
	got, err := s.ReadFile(longDest)
	if err != nil {
		t.Fatalf("ReadFile(long dest): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Error("content mismatch after move to a long name")
	}
}

func TestMoveDirectory(t *testing.T) {
	s, _ := openWritable(t)
	if err := s.Mkdir("target"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := s.Move("subdir", "target/subdir"); err != nil {
		t.Fatalf("Move directory: %v", err)
	}
	// Children keep encrypting under the moved directory's own IV, so they
	// must still be readable.
	got, err := s.ReadFile("target/subdir/nested.txt")
	if err != nil {
		t.Fatalf("ReadFile in moved directory: %v", err)
	}
	if !bytes.Equal(got, patternBytes(10, 64)) {
		t.Error("content mismatch inside a moved directory")
	}
}

func TestMoveRejectsBadDestinations(t *testing.T) {
	s, _ := openWritable(t)

	if err := s.Move("large.bin", "clip.mp4"); !errors.Is(err, ErrExists) {
		t.Errorf("Move onto existing: got %v, want ErrExists", err)
	}
	if err := s.Move("missing.bin", "x.bin"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Move of missing source: got %v, want ErrNotFound", err)
	}
	if err := s.Move("subdir", "subdir/inner"); !errors.Is(err, ErrInvalidPath) {
		t.Errorf("Move into itself: got %v, want ErrInvalidPath", err)
	}
	if err := s.Move("large.bin", "../escape.bin"); err == nil {
		t.Error("Move to a path outside the vault succeeded")
	}
}

func TestWritesRejectedOnClosedSession(t *testing.T) {
	s, _ := openWritable(t)
	s.Close()

	if _, err := s.Create("x.bin", strings.NewReader("x")); !errors.Is(err, ErrClosed) {
		t.Errorf("Create: got %v, want ErrClosed", err)
	}
	if err := s.Mkdir("x"); !errors.Is(err, ErrClosed) {
		t.Errorf("Mkdir: got %v, want ErrClosed", err)
	}
	if err := s.Remove("large.bin"); !errors.Is(err, ErrClosed) {
		t.Errorf("Remove: got %v, want ErrClosed", err)
	}
	if err := s.Move("large.bin", "y.bin"); !errors.Is(err, ErrClosed) {
		t.Errorf("Move: got %v, want ErrClosed", err)
	}
}

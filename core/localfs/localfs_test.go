package localfs

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/adrianm/imagethat/core/cache"
	"github.com/adrianm/imagethat/core/vault"
)

const pw = "localfs-test-password"

func pattern(seed byte, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*13) ^ seed
	}
	return b
}

// networkVault makes a vault holding clip.mov and opens it as if it were on a
// network mount. direct is a plain session on the same folder, standing in
// for another machine changing it.
func networkVault(t *testing.T, content []byte) (s, direct *vault.Session, cacheDir string) {
	t.Helper()
	dir := t.TempDir()
	if err := vault.Init(vault.NewLocalFS(dir), pw); err != nil {
		t.Fatal(err)
	}
	direct, err := vault.OpenLocal(dir, pw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { direct.Close() })
	if _, err := direct.Create("clip.mov", bytes.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	old := isNetwork
	isNetwork = func(string) bool { return true }
	t.Cleanup(func() { isNetwork = old })
	cacheDir = t.TempDir()
	ranges, err := cache.NewRanges(cacheDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir, pw, ranges)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, direct, cacheDir
}

func TestLocalDiskIsNotCached(t *testing.T) {
	dir := t.TempDir()
	if err := vault.Init(vault.NewLocalFS(dir), pw); err != nil {
		t.Fatal(err)
	}
	ranges, _ := cache.NewRanges(t.TempDir(), 0)
	s, err := Open(dir, pw, ranges) // a temp dir is on a local disk
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Create("a.jpg", bytes.NewReader(pattern(1, 5000))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadFile("a.jpg"); err != nil {
		t.Fatal(err)
	}
	if ranges.Size() != 0 {
		t.Fatalf("a local-disk vault put %d bytes in the network cache", ranges.Size())
	}
}

func TestNetworkMountReadsAreCached(t *testing.T) {
	want := pattern(2, 300_000)
	s, _, cacheDir := networkVault(t, want)
	got, err := s.ReadFile("clip.mov")
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("read: %v", err)
	}
	if fr := s.CachedFractions("clip.mov"); len(fr) != 1 || fr[0] != [2]float64{0, 1} {
		t.Fatalf("CachedFractions = %v, want [[0 1]]", fr)
	}
	// Only ciphertext is kept.
	entries, _ := os.ReadDir(cacheDir)
	for _, e := range entries {
		data, _ := os.ReadFile(filepath.Join(cacheDir, e.Name()))
		if bytes.Contains(data, want[:4096]) {
			t.Fatal("plaintext found in the cache")
		}
	}
}

func TestNetworkMountSeesChangesMadeElsewhere(t *testing.T) {
	s, direct, _ := networkVault(t, pattern(3, 50_000))
	if _, err := s.ReadFile("clip.mov"); err != nil {
		t.Fatal(err)
	}
	newer := pattern(4, 60_000)
	if _, err := direct.Replace("clip.mov", bytes.NewReader(newer)); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadFile("clip.mov")
	if err != nil || !bytes.Equal(got, newer) {
		t.Fatalf("read after a change elsewhere: %v, equal %v", err, bytes.Equal(got, newer))
	}
}

func TestNetworkMountWritesGoThrough(t *testing.T) {
	s, direct, _ := networkVault(t, pattern(5, 10_000))
	if _, err := s.ReadFile("clip.mov"); err != nil {
		t.Fatal(err)
	}
	edited := pattern(6, 12_000)
	if _, err := s.Replace("clip.mov", bytes.NewReader(edited)); err != nil {
		t.Fatal(err)
	}
	for name, sess := range map[string]*vault.Session{"cached": s, "direct": direct} {
		got, err := sess.ReadFile("clip.mov")
		if err != nil || !bytes.Equal(got, edited) {
			t.Fatalf("%s session reads the old content after a write: %v", name, err)
		}
	}
}

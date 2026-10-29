package cache

import (
	"bytes"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// xorSealer is a stand-in for the vault session: reversible, clearly changes
// the bytes so the on-disk form never equals the plaintext, and like a real
// AEAD refuses to open under a different key (the key byte doubles as a tag).
type xorSealer struct{ key byte }

func (x xorSealer) SealBlob(p []byte) ([]byte, error) {
	out := make([]byte, len(p)+2)
	out[0], out[1] = 0xAB, x.key
	for i, b := range p {
		out[i+2] = b ^ x.key
	}
	return out, nil
}

func (x xorSealer) OpenBlob(c []byte) ([]byte, error) {
	if len(c) < 2 || c[0] != 0xAB || c[1] != x.key {
		return nil, errors.New("bad seal")
	}
	out := make([]byte, len(c)-2)
	for i, b := range c[2:] {
		out[i] = b ^ x.key
	}
	return out, nil
}

func jpegLike(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	b[0], b[1] = 0xFF, 0xD8
	return b
}

func TestThumbDiskRoundTripAndSealing(t *testing.T) {
	root := filepath.Join(t.TempDir(), "thumbs")
	d := NewThumbDisk(root, 1<<20)
	s := xorSealer{key: 0x5A}
	jpeg := jpegLike(3000)
	key := ThumbKey("photos/a.jpg", 320, 123456)

	if _, ok := d.Get("vault-1", key, s); ok {
		t.Fatal("hit on an empty store")
	}
	if err := d.Put("vault-1", key, s, jpeg); err != nil {
		t.Fatal(err)
	}
	got, ok := d.Get("vault-1", key, s)
	if !ok || !bytes.Equal(got, jpeg) {
		t.Fatalf("Get after Put: ok=%v len=%d", ok, len(got))
	}

	// On disk: nothing readable, no JPEG marker, prefixed by the magic.
	files, _ := filepath.Glob(filepath.Join(root, "*", "*.thumb"))
	if len(files) != 1 {
		t.Fatalf("expected one entry on disk, found %d", len(files))
	}
	raw, _ := os.ReadFile(files[0])
	if !bytes.HasPrefix(raw, thumbMagic) {
		t.Error("entry lacks the magic header")
	}
	if bytes.Contains(raw, jpeg[:64]) || bytes.Contains(raw[len(thumbMagic):], []byte{0xFF, 0xD8}) {
		t.Error("entry holds the preview in the clear")
	}
	if filepath.Base(filepath.Dir(files[0])) == "vault-1" || bytes.Contains([]byte(files[0]), []byte("a.jpg")) {
		t.Error("source id or path appears unhashed on disk")
	}

	// A different sealer (another vault's key) cannot open it, and the bad
	// entry is removed.
	if _, ok := d.Get("vault-1", key, xorSealer{key: 0x11}); ok {
		t.Error("entry opened with the wrong key")
	}
	if _, err := os.Stat(files[0]); !errors.Is(err, os.ErrNotExist) {
		t.Error("undecryptable entry was not removed")
	}
	if d.Size() != 0 {
		t.Errorf("Size after removal = %d", d.Size())
	}
}

func TestThumbDiskKeyIncludesSize(t *testing.T) {
	a := ThumbKey("x.jpg", 320, 100)
	b := ThumbKey("x.jpg", 320, 101)
	c := ThumbKey("x.jpg", 321, 100)
	if a == b || a == c || b == c {
		t.Error("keys must differ by size and by edge")
	}
}

func TestThumbDiskInvalidateAndClear(t *testing.T) {
	d := NewThumbDisk(filepath.Join(t.TempDir(), "t"), 1<<20)
	s := xorSealer{key: 1}
	k1, k2 := ThumbKey("a", 64, 1), ThumbKey("b", 64, 1)
	for _, src := range []string{"s1", "s2"} {
		_ = d.Put(src, k1, s, jpegLike(500))
		_ = d.Put(src, k2, s, jpegLike(500))
	}
	d.Invalidate("s1", k1)
	if _, ok := d.Get("s1", k1, s); ok {
		t.Error("invalidated entry still present")
	}
	if _, ok := d.Get("s1", k2, s); !ok {
		t.Error("sibling entry lost")
	}
	if err := d.ClearSource("s1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := d.Get("s1", k2, s); ok {
		t.Error("ClearSource left an entry")
	}
	if _, ok := d.Get("s2", k2, s); !ok {
		t.Error("ClearSource removed another source's entry")
	}
	if err := d.Clear(); err != nil {
		t.Fatal(err)
	}
	if d.Size() != 0 {
		t.Errorf("Size after Clear = %d", d.Size())
	}
	// Clear on a missing root is fine.
	if err := NewThumbDisk(filepath.Join(t.TempDir(), "never"), 10).Clear(); err != nil {
		t.Errorf("Clear on missing root: %v", err)
	}
}

func TestThumbDiskEvictsLeastRecentlyUsed(t *testing.T) {
	d := NewThumbDisk(filepath.Join(t.TempDir(), "t"), 10*1024)
	s := xorSealer{key: 7}
	keys := make([]string, 8)
	for i := range keys {
		keys[i] = ThumbKey("f", 64, int64(i))
		if err := d.Put("src", keys[i], s, jpegLike(2000)); err != nil {
			t.Fatal(err)
		}
		// Distinct mtimes so the order is unambiguous on coarse filesystems.
		p := d.entryPath("src", keys[i])
		ts := time.Now().Add(time.Duration(i-10) * time.Second)
		_ = os.Chtimes(p, ts, ts)
		d.evictIfNeeded()
	}
	if got := d.Size(); got > 10*1024 {
		t.Errorf("Size %d exceeds budget", got)
	}
	// The oldest entries must be the ones gone; the newest must survive.
	if _, ok := d.Get("src", keys[0], s); ok {
		t.Error("oldest entry survived eviction")
	}
	if _, ok := d.Get("src", keys[7], s); !ok {
		t.Error("newest entry was evicted")
	}
	// An entry larger than the whole budget is skipped, not stored.
	if err := d.Put("src", ThumbKey("huge", 64, 1), s, jpegLike(20*1024)); err != nil {
		t.Fatal(err)
	}
	if _, ok := d.Get("src", ThumbKey("huge", 64, 1), s); ok {
		t.Error("oversized entry was stored")
	}
}

func TestThumbDiskPutIsAtomic(t *testing.T) {
	root := filepath.Join(t.TempDir(), "t")
	d := NewThumbDisk(root, 1<<20)
	s := xorSealer{key: 3}
	key := ThumbKey("p", 64, 9)
	_ = d.Put("src", key, s, jpegLike(1000))
	_ = d.Put("src", key, s, jpegLike(1000))
	leftovers, _ := filepath.Glob(filepath.Join(root, "*", ".tmp-*"))
	if len(leftovers) != 0 {
		t.Errorf("temp files left behind: %v", leftovers)
	}
	files, _ := filepath.Glob(filepath.Join(root, "*", "*.thumb"))
	if len(files) != 1 {
		t.Errorf("overwrite left %d entries, want 1", len(files))
	}
}

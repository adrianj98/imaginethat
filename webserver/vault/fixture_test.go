package vault

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The test vault in testdata/vault was produced by the real gocryptfs v2.6.1
// binary, not by this package. Reading it back therefore checks the port in
// this package against genuine upstream ciphertext rather than against itself.
//
// To regenerate (needs the gocryptfs binary and FUSE):
//
//	gocryptfs -init -passfile pw cipher
//	gocryptfs -passfile pw cipher mnt
//	VAULT_GEN_FIXTURE=mnt go test ./vault/ -run TestGenerateFixture
//	fusermount -u mnt   # or: umount mnt
//	rm -rf vault/testdata/vault && cp -R cipher vault/testdata/vault
const (
	fixturePassword = "test-password-123"
	fixtureDir      = "testdata/vault"
)

// fixtureKind selects how a fixture file's content is produced.
type fixtureKind int

const (
	// kindPattern is deterministic pseudo-random bytes.
	kindPattern fixtureKind = iota
	// kindJPEG and kindPNG are real, decodable images so the thumbnail path
	// can be exercised against genuine image data.
	kindJPEG
	kindPNG
)

// fixtureFile is one file in the test vault.
type fixtureFile struct {
	Path string
	Size int // pattern files only
	Seed uint64
	Kind fixtureKind
}

// Content returns the file's expected plaintext.
func (f fixtureFile) Content() []byte {
	switch f.Kind {
	case kindJPEG:
		return encodeFixtureImage(f.Seed, kindJPEG)
	case kindPNG:
		return encodeFixtureImage(f.Seed, kindPNG)
	default:
		return patternBytes(f.Seed, f.Size)
	}
}

// fixtureFiles covers the cases where the block arithmetic is easy to get
// wrong: an empty file (no header at all), a single byte, and the boundaries
// either side of the 4096-byte plaintext block size.
func fixtureFiles() []fixtureFile {
	return []fixtureFile{
		{Path: "empty.bin", Size: 0, Seed: 1},
		{Path: "onebyte.bin", Size: 1, Seed: 2},
		{Path: "block-4095.bin", Size: 4095, Seed: 3},
		{Path: "block-4096.bin", Size: 4096, Seed: 4},
		{Path: "block-4097.bin", Size: 4097, Seed: 5},
		{Path: "block-8192.bin", Size: 8192, Seed: 6},
		{Path: "large.bin", Size: 100000, Seed: 7},
		{Path: "clip.mp4", Size: 60000, Seed: 9},
		{Path: "photo.jpg", Seed: 8, Kind: kindJPEG},
		{Path: "photo.png", Seed: 14, Kind: kindPNG},
		{Path: "subdir/nested.txt", Size: 64, Seed: 10},
		{Path: "subdir/deeper/deep.bin", Size: 12345, Seed: 11},
		// A plaintext name long enough that its EME ciphertext exceeds
		// longNameMax, forcing the gocryptfs.longname.<hash> + .name sidecar
		// representation.
		{Path: longFixtureName, Size: 5000, Seed: 12},
		{Path: "subdir/" + longFixtureName, Size: 777, Seed: 13},
	}
}

// encodeFixtureImage builds a small deterministic gradient image and encodes
// it. Go's encoders are deterministic, so the bytes are stable across runs.
func encodeFixtureImage(seed uint64, kind fixtureKind) []byte {
	const w, h = 160, 120
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{
				R: uint8((x*3 + int(seed)*7) % 256),
				G: uint8((y*5 + int(seed)*11) % 256),
				B: uint8((x*y + int(seed)*13) % 256),
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	if kind == kindPNG {
		if err := png.Encode(&buf, img); err != nil {
			panic(err)
		}
	} else {
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
			panic(err)
		}
	}
	return buf.Bytes()
}

// longFixtureName is 200 characters, well past the point where the encrypted
// name gets hashed.
var longFixtureName = strings.Repeat("long-file-name-segment-", 8) + "tail.bin"

// fixtureDirs are the directories the fixture expects to exist.
func fixtureDirs() []string {
	return []string{"subdir", "subdir/deeper", "emptydir"}
}

// patternBytes produces deterministic pseudo-random content by chaining
// SHA-256, so tests can recompute the expected plaintext for any byte range
// without storing it.
func patternBytes(seed uint64, n int) []byte {
	out := make([]byte, 0, n+sha256.Size)
	block := make([]byte, 8)
	binary.LittleEndian.PutUint64(block, seed)
	h := sha256.Sum256(block)
	for len(out) < n {
		out = append(out, h[:]...)
		h = sha256.Sum256(h[:])
	}
	return out[:n]
}

// TestGenerateFixture writes the fixture's plaintext into a mounted gocryptfs
// filesystem. It is skipped unless VAULT_GEN_FIXTURE points at the mountpoint.
func TestGenerateFixture(t *testing.T) {
	mnt := os.Getenv("VAULT_GEN_FIXTURE")
	if mnt == "" {
		t.Skip("set VAULT_GEN_FIXTURE=<mountpoint> to regenerate the test vault")
	}
	for _, dir := range fixtureDirs() {
		if err := os.MkdirAll(filepath.Join(mnt, dir), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	for _, f := range fixtureFiles() {
		dst := filepath.Join(mnt, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			t.Fatalf("mkdir for %s: %v", f.Path, err)
		}
		if err := os.WriteFile(dst, f.Content(), 0o600); err != nil {
			t.Fatalf("write %s: %v", f.Path, err)
		}
	}
	t.Logf("wrote %d files and %d dirs into %s", len(fixtureFiles()), len(fixtureDirs()), mnt)
}

// openFixture unlocks the checked-in test vault.
func openFixture(t *testing.T) *Session {
	t.Helper()
	if _, err := os.Stat(filepath.Join(fixtureDir, "gocryptfs.conf")); err != nil {
		t.Skipf("test vault missing at %s (see fixture_test.go for how to regenerate)", fixtureDir)
	}
	s, err := OpenLocal(fixtureDir, fixturePassword)
	if err != nil {
		t.Fatalf("OpenLocal: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

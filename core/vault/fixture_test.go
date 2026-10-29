package vault

import (
	"crypto/sha256"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The test vaults under testdata/ were produced by the real gocryptfs v2.6.1
// binary, not by this package. Reading them back therefore checks the port in
// this package against genuine upstream ciphertext rather than against itself.
//
// There is one vault per content cipher, because they are not interchangeable:
// AES-SIV in particular takes a different branch in CryptoCore.Wipe, and a
// GCM-only fixture hid a panic there.
//
// The image plaintexts live in testdata/plain/ and are authoritative: they
// were extracted from these vaults (and checked byte-for-byte against the
// Go 1.25 encoders that originally produced them). If the vaults are
// regenerated, TestGenerateFixture writes those committed bytes into the new
// vault, so the two stay in step. Do not regenerate the images from
// image/jpeg or image/png.
//
// To regenerate (needs the gocryptfs binary and FUSE) — see
// scripts/gen-fixtures.sh, which does all of this:
//
//	gocryptfs -init [-aessiv|-xchacha] -passfile pw cipher
//	gocryptfs -passfile pw cipher mnt
//	VAULT_GEN_FIXTURE=mnt VAULT_GEN_SET=full go test ./vault/ -run TestGenerateFixture
//	umount mnt
//	rm -rf vault/testdata/<name> && cp -R cipher vault/testdata/<name>
const (
	fixturePassword = "test-password-123"
	fixtureDir      = "testdata/vault"
	// Vaults using the two non-default content ciphers. They carry a smaller
	// file set — their job is to cover the cipher, not to re-test the block
	// arithmetic the main fixture already covers exhaustively.
	fixtureDirAESSIV  = "testdata/vault-aessiv"
	fixtureDirXChaCha = "testdata/vault-xchacha"
)

// altFixtureVaults lists the alternative-cipher vaults, for tests that should
// run against every content cipher.
func altFixtureVaults() map[string]string {
	return map[string]string{
		"aessiv":  fixtureDirAESSIV,
		"xchacha": fixtureDirXChaCha,
	}
}

// fixtureFile is one file in the test vault.
//
// Most files are deterministic pseudo-random bytes (patternBytes: SHA-256
// chaining, stable across Go versions). The images are different: they are
// committed under testdata/plain/ and read back verbatim. They used to be
// produced at test time with image/jpeg and image/png, but Go 1.26 changed
// those encoders' output, so the expected bytes drifted away from what the
// gocryptfs-written fixture vaults hold. The committed bytes are therefore the
// authoritative plaintext; nothing re-encodes them.
type fixtureFile struct {
	Path string
	Size int // pattern files only
	Seed uint64
	// Plain, when set, is a path under testdata/plain/ holding the exact
	// plaintext stored in the fixture vaults. Size and Seed are ignored.
	Plain string
}

// Content returns the file's expected plaintext.
func (f fixtureFile) Content() []byte {
	if f.Plain != "" {
		b, err := os.ReadFile(filepath.Join("testdata", "plain", filepath.FromSlash(f.Plain)))
		if err != nil {
			panic(err)
		}
		return b
	}
	return patternBytes(f.Seed, f.Size)
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
		{Path: "photo.jpg", Plain: "full/photo.jpg"},
		{Path: "photo.png", Plain: "full/photo.png"},
		{Path: "subdir/nested.txt", Size: 64, Seed: 10},
		{Path: "subdir/deeper/deep.bin", Size: 12345, Seed: 11},
		// A plaintext name long enough that its EME ciphertext exceeds
		// longNameMax, forcing the gocryptfs.longname.<hash> + .name sidecar
		// representation.
		{Path: longFixtureName, Size: 5000, Seed: 12},
		{Path: "subdir/" + longFixtureName, Size: 777, Seed: 13},
	}
}

// longFixtureName is 200 characters, well past the point where the encrypted
// name gets hashed.
var longFixtureName = strings.Repeat("long-file-name-segment-", 8) + "tail.bin"

// fixtureDirs are the directories the fixture expects to exist.
func fixtureDirs() []string {
	return []string{"subdir", "subdir/deeper", "emptydir"}
}

// altFixtureFiles is the reduced set written into the alternative-cipher
// vaults. It still spans a block boundary, a long name and a subdirectory, but
// keeps those vaults small enough to be worth committing.
func altFixtureFiles() []fixtureFile {
	return []fixtureFile{
		{Path: "onebyte.bin", Size: 1, Seed: 101},
		{Path: "block-4096.bin", Size: 4096, Seed: 102},
		{Path: "block-4097.bin", Size: 4097, Seed: 103},
		{Path: "medium.bin", Size: 20000, Seed: 104},
		{Path: "photo.jpg", Plain: "alt/photo.jpg"},
		{Path: "subdir/nested.bin", Size: 3000, Seed: 106},
		{Path: longFixtureName, Size: 2000, Seed: 107},
	}
}

func altFixtureDirs() []string {
	return []string{"subdir"}
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

// TestGenerateFixture writes a fixture's plaintext into a mounted gocryptfs
// filesystem. It is skipped unless VAULT_GEN_FIXTURE points at the mountpoint.
// VAULT_GEN_SET picks the file set: "full" (default) or "alt".
func TestGenerateFixture(t *testing.T) {
	mnt := os.Getenv("VAULT_GEN_FIXTURE")
	if mnt == "" {
		t.Skip("set VAULT_GEN_FIXTURE=<mountpoint> to regenerate a test vault")
	}

	files, dirs := fixtureFiles(), fixtureDirs()
	if os.Getenv("VAULT_GEN_SET") == "alt" {
		files, dirs = altFixtureFiles(), altFixtureDirs()
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(filepath.Join(mnt, dir), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	for _, f := range files {
		dst := filepath.Join(mnt, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			t.Fatalf("mkdir for %s: %v", f.Path, err)
		}
		if err := os.WriteFile(dst, f.Content(), 0o600); err != nil {
			t.Fatalf("write %s: %v", f.Path, err)
		}
	}
	t.Logf("wrote %d files and %d dirs into %s", len(files), len(dirs), mnt)
}

// openFixture unlocks the checked-in test vault.
func openFixture(t *testing.T) *Session {
	t.Helper()
	return openVault(t, fixtureDir)
}

// openVault unlocks a checked-in fixture vault by directory.
func openVault(t *testing.T, dir string) *Session {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, "gocryptfs.conf")); err != nil {
		t.Skipf("test vault missing at %s (see fixture_test.go for how to regenerate)", dir)
	}
	s, err := OpenLocal(dir, fixturePassword)
	if err != nil {
		t.Fatalf("OpenLocal(%s): %v", dir, err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

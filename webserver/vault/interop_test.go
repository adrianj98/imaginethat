package vault

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestInteropWithGocryptfsBinary is the ground truth for the write path.
//
// The other write tests only prove this package can read back what it wrote,
// which would still pass if the ciphertext were subtly non-standard. Here the
// real gocryptfs binary mounts a vault this package wrote to and has to agree
// about every byte.
//
// Skipped unless the gocryptfs binary and a usable FUSE device are present.
func TestInteropWithGocryptfsBinary(t *testing.T) {
	bin := requireGocryptfs(t)

	dir := copyFixtureVault(t)
	s, err := OpenLocal(dir, fixturePassword)
	if err != nil {
		t.Fatalf("OpenLocal: %v", err)
	}

	longName := strings.Repeat("interop-long-name-part-", 9) + "end.bin"
	written := map[string][]byte{
		"interop-small.bin":       patternBytes(2001, 10),
		"interop-block.bin":       patternBytes(2002, 4096),
		"interop-overflow.bin":    patternBytes(2003, 4097),
		"interop-big.bin":         patternBytes(2004, 250000),
		"interop-empty.bin":       {},
		"interop-dir/nested.bin":  patternBytes(2005, 33333),
		longName:                  patternBytes(2006, 7000),
		"interop-dir/" + longName: patternBytes(2007, 1234),
	}

	if err := s.Mkdir("interop-dir"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	for name, content := range written {
		if _, err := s.Create(name, bytes.NewReader(content)); err != nil {
			t.Fatalf("Create(%s): %v", name, err)
		}
	}

	// Exercise the rename path too — it rewrites the encrypted name under the
	// destination directory's IV, which the binary must be able to decrypt.
	if err := s.Move("interop-small.bin", "interop-dir/renamed.bin"); err != nil {
		t.Fatalf("Move: %v", err)
	}
	written["interop-dir/renamed.bin"] = written["interop-small.bin"]
	delete(written, "interop-small.bin")

	// And deletion, so the binary must not trip over an orphaned sidecar.
	if err := s.Remove("large.bin"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	s.Close()

	mnt := mountWithGocryptfs(t, bin, dir)

	for name, want := range written {
		got, err := os.ReadFile(filepath.Join(mnt, filepath.FromSlash(name)))
		if err != nil {
			t.Errorf("gocryptfs could not read %s: %v", name, err)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: gocryptfs read %d bytes, we wrote %d — content differs",
				name, len(got), len(want))
		}
	}

	if _, err := os.Stat(filepath.Join(mnt, "large.bin")); !os.IsNotExist(err) {
		t.Errorf("deleted file still visible to gocryptfs: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mnt, "interop-small.bin")); !os.IsNotExist(err) {
		t.Error("moved file still visible at its old path")
	}

	// The fixture's original content must survive our writes untouched.
	orig, err := os.ReadFile(filepath.Join(mnt, "clip.mp4"))
	if err != nil {
		t.Fatalf("read pre-existing file: %v", err)
	}
	if !bytes.Equal(orig, patternBytes(9, 60000)) {
		t.Error("pre-existing file changed")
	}
}

// requireGocryptfs returns the path to the gocryptfs binary, skipping the test
// if it or FUSE is unavailable.
func requireGocryptfs(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("interop test needs FUSE; skipped in -short mode")
	}
	bin, err := exec.LookPath("gocryptfs")
	if err != nil {
		t.Skip("gocryptfs binary not on PATH; install github.com/rfjakob/gocryptfs/v2@v2.6.1 to run interop tests")
	}
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skip("/dev/fuse not available; cannot mount")
	}
	return bin
}

// mountWithGocryptfs mounts cipherDir with the real binary and returns the
// mountpoint, unmounting on cleanup.
func mountWithGocryptfs(t *testing.T, bin, cipherDir string) string {
	t.Helper()

	pwFile := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(pwFile, []byte(fixturePassword), 0o600); err != nil {
		t.Fatalf("write passfile: %v", err)
	}
	mnt := t.TempDir()

	cmd := exec.Command(bin, "-passfile", pwFile, "-q", cipherDir, mnt)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("could not mount with gocryptfs (%v): %s", err, out)
	}

	t.Cleanup(func() {
		for _, unmount := range [][]string{
			{"fusermount", "-u", mnt},
			{"fusermount3", "-u", mnt},
			{"umount", mnt},
		} {
			if _, err := exec.LookPath(unmount[0]); err != nil {
				continue
			}
			if err := exec.Command(unmount[0], unmount[1:]...).Run(); err == nil {
				return
			}
		}
		t.Logf("warning: could not unmount %s", mnt)
	})

	// Give the mount a moment to become visible before the test reads it.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if entries, err := os.ReadDir(mnt); err == nil && len(entries) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	return mnt
}

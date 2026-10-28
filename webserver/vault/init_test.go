package vault

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInitThenOpen(t *testing.T) {
	dir := t.TempDir()
	if err := Init(NewLocalFS(dir), "pw-123456"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	s, err := Open(NewLocalFS(dir), "pw-123456")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	if _, err := s.Create("a.txt", strings.NewReader("hello")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := s.ReadFile("a.txt")
	if err != nil || string(got) != "hello" {
		t.Fatalf("ReadFile = %q, %v; want hello", got, err)
	}
	if _, err := Open(NewLocalFS(dir), "wrong"); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("Open wrong password = %v; want ErrWrongPassword", err)
	}
}

func TestInitRefusesExistingVault(t *testing.T) {
	dir := t.TempDir()
	if err := Init(NewLocalFS(dir), "pw-123456"); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "gocryptfs.conf"))
	err := Init(NewLocalFS(dir), "other-pw")
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("second Init = %v; want error wrapping fs.ErrExist", err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "gocryptfs.conf"))
	if !bytes.Equal(before, after) {
		t.Fatal("gocryptfs.conf changed by refused Init")
	}
}

func TestOpenNoConfig(t *testing.T) {
	if _, err := Open(NewLocalFS(t.TempDir()), "pw"); !errors.Is(err, ErrNoConfig) {
		t.Fatalf("Open empty dir = %v; want ErrNoConfig", err)
	}
}

func TestInitInteropWithGocryptfs(t *testing.T) {
	if testing.Short() {
		t.Skip("interop test needs FUSE; skipped in -short mode")
	}
	bin, err := exec.LookPath("gocryptfs")
	if err != nil {
		t.Skip("gocryptfs binary not on PATH")
	}
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skip("/dev/fuse not available; cannot mount")
	}

	const pw = "pw-123456"
	dir := t.TempDir()
	if err := Init(NewLocalFS(dir), pw); err != nil {
		t.Fatalf("Init: %v", err)
	}
	pwFile := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(pwFile, []byte(pw), 0o600); err != nil {
		t.Fatal(err)
	}
	// Not t.TempDir: if a mount gets stuck its cleanup would fail the test.
	mnt, err := os.MkdirTemp("", "init-mnt-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(mnt)
	unmounted := false
	unmount := func() {
		if unmounted {
			return
		}
		for _, c := range [][]string{{"fusermount", "-u", mnt}, {"fusermount3", "-u", mnt}, {"umount", mnt}} {
			if _, err := exec.LookPath(c[0]); err != nil {
				continue
			}
			if exec.Command(c[0], c[1:]...).Run() == nil {
				unmounted = true
				return
			}
		}
		t.Logf("warning: could not unmount %s", mnt)
	}
	defer unmount()
	// A FUSE mount that is not approved (macFUSE before its system extension
	// is allowed) hangs instead of failing, so bound it and skip.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	mountCmd := exec.CommandContext(ctx, bin, "-passfile", pwFile, "-q", dir, mnt)
	mountCmd.WaitDelay = time.Second
	if out, err := mountCmd.CombinedOutput(); err != nil {
		unmount()
		t.Skipf("could not mount with gocryptfs (%v): %s", err, out)
	}

	want := []byte("written through the real gocryptfs mount")
	if err := os.WriteFile(filepath.Join(mnt, "from-mount.txt"), want, 0o600); err != nil {
		t.Fatalf("write via mount: %v", err)
	}
	unmount()

	s, err := Open(NewLocalFS(dir), pw)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	got, err := s.ReadFile("from-mount.txt")
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("ReadFile = %q, %v; want %q", got, err, want)
	}
}

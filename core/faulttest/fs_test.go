package faulttest

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/adrianm/imagethat/core/vault"
	"github.com/adrianm/imagethat/core/xfer"
)

// newFS is an FS over a temp directory holding one 10 000-byte file, "f".
func newFS(t *testing.T) (*FS, string) {
	t.Helper()
	dir := t.TempDir()
	f := NewFS(vault.NewLocalFS(dir))
	if err := f.WriteFile("f", pattern(10_000)); err != nil {
		t.Fatal(err)
	}
	return f, dir
}

func TestReadFaultCalls(t *testing.T) {
	f, _ := newFS(t)
	f.FailReads(ReadFault{From: 4096, Calls: 2, Err: syscall.EIO})
	buf := make([]byte, 100)
	if _, err := f.ReadAt("f", 0, buf); err != nil {
		t.Fatalf("read before From: %v", err)
	}
	if f.Fired() {
		t.Error("Fired before any failure")
	}
	for i := range 2 {
		if _, err := f.ReadAt("f", 5000, buf); !errors.Is(err, syscall.EIO) {
			t.Fatalf("failing read %d: %v, want EIO", i+1, err)
		}
	}
	if n, err := f.ReadAt("f", 5000, buf); err != nil || n != 100 {
		t.Fatalf("third read: %d, %v", n, err)
	}
	if !f.Fired() {
		t.Error("Fired is false")
	}
}

func TestReadFaultCoversAReadThatReachesFrom(t *testing.T) {
	f, _ := newFS(t)
	f.FailReads(ReadFault{From: 9_999, Calls: 1, Err: syscall.EIO})
	if _, err := f.ReadAt("f", 9_000, make([]byte, 1000)); !errors.Is(err, syscall.EIO) {
		t.Fatalf("read ending on the last byte: %v, want EIO", err)
	}
}

func TestReadFaultFor(t *testing.T) {
	f, _ := newFS(t)
	f.FailReads(ReadFault{For: 100 * time.Millisecond, Err: syscall.ETIMEDOUT})
	buf := make([]byte, 100)
	if _, err := f.ReadAt("f", 0, buf); !errors.Is(err, syscall.ETIMEDOUT) {
		t.Fatalf("first read: %v, want ETIMEDOUT", err)
	}
	if _, err := f.ReadAt("f", 0, buf); !errors.Is(err, syscall.ETIMEDOUT) {
		t.Fatalf("second read inside the window: %v", err)
	}
	time.Sleep(120 * time.Millisecond)
	if _, err := f.ReadAt("f", 0, buf); err != nil {
		t.Fatalf("read after the window: %v", err)
	}
}

func TestReadFaultShort(t *testing.T) {
	f, _ := newFS(t)
	f.FailReads(ReadFault{Calls: 1, Short: true})
	if n, err := f.ReadAt("f", 0, make([]byte, 100)); n != 0 || err != nil {
		t.Fatalf("short read: %d, %v; want 0, nil", n, err)
	}
}

func TestClearEndsFaults(t *testing.T) {
	f, _ := newFS(t)
	f.FailReads(ReadFault{For: time.Hour, Err: syscall.EIO})
	f.FailRename(5, false, syscall.EIO)
	f.Clear()
	if _, err := f.ReadAt("f", 0, make([]byte, 100)); err != nil {
		t.Fatalf("read after Clear: %v", err)
	}
	if err := f.Rename("f", "g"); err != nil {
		t.Fatalf("rename after Clear: %v", err)
	}
}

func TestWriteFault(t *testing.T) {
	f, _ := newFS(t)
	f.FailWrites(1, 3000, syscall.ENOSPC)
	w, _, err := f.CreateTemp("", "tmp-*")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(pattern(2000)); err != nil {
		t.Fatalf("write below the limit: %v", err)
	}
	if _, err := w.Write(pattern(3000)); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("write past the limit: %v, want ENOSPC", err)
	}
	w.Close()
	if !f.Fired() {
		t.Error("Fired is false")
	}

	w, _, err = f.CreateTemp("", "tmp-*")
	if err != nil {
		t.Fatal(err)
	}
	if n, err := w.Write(pattern(5000)); n != 5000 || err != nil {
		t.Fatalf("the next temp file: %d, %v", n, err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCloseFault(t *testing.T) {
	f, _ := newFS(t)
	f.FailClose(1, syscall.EIO)
	for i, want := range []error{syscall.EIO, nil} {
		w, _, err := f.CreateTemp("", "tmp-*")
		if err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); !errors.Is(err, want) {
			t.Fatalf("close %d: %v, want %v", i+1, err, want)
		}
	}
}

func TestRenameFailed(t *testing.T) {
	f, dir := newFS(t)
	f.FailRename(1, false, syscall.EIO)
	if err := f.Rename("f", "g"); !errors.Is(err, syscall.EIO) {
		t.Fatalf("rename: %v, want EIO", err)
	}
	if !f.Exists("f") || f.Exists("g") {
		t.Error("a failed rename moved the file")
	}
	if err := f.Rename("f", "g"); err != nil {
		t.Fatalf("second rename: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "g")); err != nil {
		t.Error(err)
	}
}

func TestRenameLanded(t *testing.T) {
	f, _ := newFS(t)
	f.FailRename(1, true, syscall.ETIMEDOUT)
	if err := f.Rename("f", "g"); !errors.Is(err, syscall.ETIMEDOUT) {
		t.Fatalf("rename: %v, want ETIMEDOUT", err)
	}
	if f.Exists("f") || !f.Exists("g") {
		t.Error("the rename did not land")
	}
}

func TestFlakyOpener(t *testing.T) {
	path := filepath.Join(t.TempDir(), "src")
	if err := os.WriteFile(path, pattern(10_000), 0o600); err != nil {
		t.Fatal(err)
	}
	open, flaky := FlakyOpener(xfer.OpenFile(path), ReadFault{From: 4096, Calls: 1, Err: syscall.EIO})
	src, err := open()
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if src.Size() != 10_000 {
		t.Errorf("Size = %d", src.Size())
	}
	buf := make([]byte, 100)
	if _, err := src.ReadAt(buf, 0); err != nil || flaky.Fired() {
		t.Fatalf("read before From: %v, fired %v", err, flaky.Fired())
	}
	if _, err := src.ReadAt(buf, 5000); !errors.Is(err, syscall.EIO) {
		t.Fatalf("read past From: %v, want EIO", err)
	}
	if !flaky.Fired() {
		t.Error("Fired is false")
	}

	// The count carries across reopens, and Clear ends a fault early.
	flaky.Set(ReadFault{For: time.Hour, Err: syscall.EIO})
	again, err := open()
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if _, err := again.ReadAt(buf, 0); !errors.Is(err, syscall.EIO) {
		t.Fatalf("read after Set: %v, want EIO", err)
	}
	flaky.Clear()
	if _, err := again.ReadAt(buf, 0); err != nil {
		t.Fatalf("read after Clear: %v", err)
	}
}

package cache

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/adrianm/imagethat/core/xfer"
)

type memSource struct {
	data []byte
	fail bool
}

func (m memSource) Size() int64  { return int64(len(m.data)) }
func (m memSource) Close() error { return nil }
func (m memSource) ReadAt(p []byte, off int64) (int, error) {
	if m.fail && off > 0 {
		return 0, errors.New("EIO")
	}
	if off >= int64(len(m.data)) {
		return 0, io.EOF
	}
	n := copy(p, m.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func opener(src memSource) xfer.Opener {
	return func() (xfer.Source, error) { return src, nil }
}

func TestFilesFetchOpenRemove(t *testing.T) {
	dir := t.TempDir()
	f, err := NewFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("cipher"), 50_000)
	if err := f.Fetch(context.Background(), "k", opener(memSource{data: data}), xfer.Options{}); err != nil {
		t.Fatal(err)
	}
	if size, ok := f.Size("k"); !ok || size != int64(len(data)) {
		t.Fatalf("Size = %d, %v", size, ok)
	}
	h, err := f.Open("k")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(h)
	h.Close()
	if !bytes.Equal(got, data) {
		t.Fatal("cached bytes differ")
	}
	if FilesSize(dir) != int64(len(data)) {
		t.Fatalf("FilesSize = %d", FilesSize(dir))
	}
	if err := f.Remove("k"); err != nil || f.Has("k") {
		t.Fatalf("Remove: %v, still there: %v", err, f.Has("k"))
	}
	if err := f.Remove("k"); err != nil {
		t.Fatalf("removing a missing entry: %v", err)
	}
}

func TestFilesFailedFetchLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	f, _ := NewFiles(dir)
	opts := xfer.Options{Attempts: 2, ChunkSize: 1000, BaseDelay: time.Nanosecond, MaxDelay: time.Nanosecond}
	err := f.Fetch(context.Background(), "k", opener(memSource{data: make([]byte, 10_000), fail: true}), opts)
	if err == nil {
		t.Fatal("want an error")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("%d files left after a failed fetch", len(entries))
	}
}

func TestNewFilesRemovesOnlyStaleTemps(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, filesTempPrefix+"old")
	fresh := filepath.Join(dir, filesTempPrefix+"new")
	os.WriteFile(stale, []byte("x"), 0o600)
	os.WriteFile(fresh, []byte("x"), 0o600)
	old := time.Now().Add(-2 * staleTemp)
	os.Chtimes(stale, old, old)
	if _, err := NewFiles(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("stale temp kept")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("a fetch in progress had its temp file removed")
	}
	if err := ClearFiles(dir); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatal("ClearFiles left files")
	}
}

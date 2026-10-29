package vault

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/adrianm/imagethat/core/xfer"
)

// blipSource serves data and fails every failEvery-th ReadAt.
type blipSource struct {
	data      []byte
	failEvery int
	calls     *int
}

func (b blipSource) Size() int64  { return int64(len(b.data)) }
func (b blipSource) Close() error { return nil }
func (b blipSource) ReadAt(p []byte, off int64) (int, error) {
	*b.calls++
	if b.failEvery > 0 && *b.calls%b.failEvery == 0 {
		return 0, errors.New("network blip")
	}
	if off >= int64(len(b.data)) {
		return 0, io.EOF
	}
	n := copy(p, b.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func blipOpener(data []byte, failEvery int) xfer.Opener {
	calls := new(int)
	return func() (xfer.Source, error) { return blipSource{data, failEvery, calls}, nil }
}

// quick makes retries instant: a real sleep would only slow the tests.
func quick() xfer.Options {
	return xfer.Options{ChunkSize: 8192, BaseDelay: time.Nanosecond, MaxDelay: time.Nanosecond}
}

func TestCopyInSurvivesTransientErrors(t *testing.T) {
	s, _ := openWritable(t)
	want := patternBytes(7, 300_000)
	var p xfer.Progress
	opts := quick()
	opts.Progress = &p
	n, err := s.CopyIn(context.Background(), "flaky.bin", blipOpener(want, 4), opts)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(want)) {
		t.Fatalf("wrote %d, want %d", n, len(want))
	}
	got, err := s.ReadFile("flaky.bin")
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("read back: err %v, equal %v", err, bytes.Equal(got, want))
	}
	if _, _, retries := p.Snapshot(); retries == 0 {
		t.Fatal("expected retries to be counted")
	}
}

func TestCopyInFailureLeavesNothing(t *testing.T) {
	s, dir := openWritable(t)
	opts := quick()
	opts.Attempts = 2
	// Every second read fails, so two in a row happen after a reopen.
	data := patternBytes(8, 100_000)
	calls := 0
	open := func() (xfer.Source, error) {
		return failAfter{blipSource{data, 0, &calls}, 30_000}, nil
	}
	if _, err := s.CopyIn(context.Background(), "doomed.bin", open, opts); err == nil {
		t.Fatal("CopyIn should have failed")
	}
	if _, err := s.FileSize("doomed.bin"); err == nil {
		t.Fatal("a failed copy left a visible file")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".imagethat-upload-") {
			t.Fatalf("temp file %q left behind", e.Name())
		}
	}
}

// failAfter fails every read at or beyond limit.
type failAfter struct {
	blipSource
	limit int64
}

func (f failAfter) ReadAt(p []byte, off int64) (int, error) {
	if off >= f.limit {
		return 0, errors.New("EIO")
	}
	return f.blipSource.ReadAt(p, off)
}

func TestCopyInCancelled(t *testing.T) {
	s, _ := openWritable(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.CopyIn(ctx, "cancelled.bin", blipOpener(patternBytes(9, 50_000), 0), quick()); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if _, err := s.FileSize("cancelled.bin"); err == nil {
		t.Fatal("a cancelled copy left a visible file")
	}
}

func TestCopyInRefusesExisting(t *testing.T) {
	s, _ := openWritable(t)
	if _, err := s.CopyIn(context.Background(), "a.bin", blipOpener([]byte("x"), 0), quick()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CopyIn(context.Background(), "a.bin", blipOpener([]byte("y"), 0), quick()); !errors.Is(err, ErrExists) {
		t.Fatalf("err = %v, want ErrExists", err)
	}
}

func TestCopyBetweenVaults(t *testing.T) {
	src, _ := openWritable(t)
	dst, _ := openWritable(t)
	want := patternBytes(10, 70_000)
	if _, err := src.Create("from.bin", bytes.NewReader(want)); err != nil {
		t.Fatal(err)
	}
	if _, err := dst.CopyIn(context.Background(), "to.bin", src.Opener("from.bin"), quick()); err != nil {
		t.Fatal(err)
	}
	got, err := dst.ReadFile("to.bin")
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("err %v, equal %v", err, bytes.Equal(got, want))
	}
	// A missing source is permanent: no retry loop.
	if _, err := dst.CopyIn(context.Background(), "x.bin", src.Opener("nope.bin"), quick()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// countingVFS counts the ciphertext bytes read through it.
type countingVFS struct {
	VFS
	read *int64
}

func (c countingVFS) ReadAt(p string, off int64, buf []byte) (int, error) {
	n, err := c.VFS.ReadAt(p, off, buf)
	*c.read += int64(n)
	return n, err
}

func TestCacheFileReadsThroughWithoutCacher(t *testing.T) {
	src, dir := openWritable(t)
	if _, err := src.Create("big.bin", bytes.NewReader(patternBytes(11, 100_000))); err != nil {
		t.Fatal(err)
	}
	read := new(int64)
	s, err := Open(countingVFS{NewLocalFS(dir), read}, fixturePassword)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CacheFile(context.Background(), "big.bin", quick()); err != nil {
		t.Fatal(err)
	}
	cipherPath, _ := s.CipherPath("big.bin")
	size, _ := NewLocalFS(dir).FileSize(cipherPath)
	if *read != size {
		t.Fatalf("read %d ciphertext bytes, want the whole file (%d)", *read, size)
	}
	if s.IsCached("big.bin") {
		t.Fatal("IsCached is true without a Cacher backend")
	}
}

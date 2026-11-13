package vault

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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

// writeFaults makes the storage under a session fail on demand.
type writeFaults struct {
	WriteVFS
	temps      *int
	failTemps  int // this many temp files die partway through
	writeErr   error
	loseCommit *bool // the next Rename lands but reports failure
}

func (w writeFaults) CreateTemp(dir, pattern string) (io.WriteCloser, string, error) {
	out, p, err := w.WriteVFS.CreateTemp(dir, pattern)
	if err != nil {
		return nil, "", err
	}
	*w.temps++
	if *w.temps > w.failTemps {
		return out, p, nil
	}
	return &dyingWriter{out, 20_000, w.writeErr}, p, nil
}

func (w writeFaults) Rename(from, to string) error {
	err := w.WriteVFS.Rename(from, to)
	if err == nil && w.loseCommit != nil && *w.loseCommit {
		*w.loseCommit = false
		return errors.New("connection lost")
	}
	return err
}

type dyingWriter struct {
	io.WriteCloser
	left int
	err  error
}

func (d *dyingWriter) Write(p []byte) (int, error) {
	if d.left -= len(p); d.left < 0 {
		return 0, d.err
	}
	return d.WriteCloser.Write(p)
}

func leftoverTemps(t *testing.T, dir string) []string {
	t.Helper()
	found, err := filepath.Glob(filepath.Join(dir, uploadTempPrefix+"*"))
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func TestCopyInStartsOverAfterWriteFailure(t *testing.T) {
	s, dir := openWritable(t)
	temps := 0
	s.fs = writeFaults{WriteVFS: s.fs.(WriteVFS), temps: &temps, failTemps: 2, writeErr: errors.New("share went away")}
	want := patternBytes(11, 300_000)
	var p xfer.Progress
	opts := quick()
	opts.Progress = &p

	if _, err := s.CopyIn(context.Background(), "again.bin", blipOpener(want, 0), opts); err != nil {
		t.Fatal(err)
	}
	if temps != 3 {
		t.Errorf("wrote %d temp files, want 3", temps)
	}
	got, err := s.ReadFile("again.bin")
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("read back: err %v, equal %v", err, bytes.Equal(got, want))
	}
	if done, _, retries := p.Snapshot(); done != int64(len(want)) || retries != 2 {
		t.Errorf("progress done %d retries %d", done, retries)
	}
	if left := leftoverTemps(t, dir); len(left) != 0 {
		t.Errorf("temp files left: %v", left)
	}
}

func TestCopyInDoesNotRetryFullDisk(t *testing.T) {
	s, _ := openWritable(t)
	temps := 0
	s.fs = writeFaults{WriteVFS: s.fs.(WriteVFS), temps: &temps, failTemps: 99, writeErr: syscall.ENOSPC}
	_, err := s.CopyIn(context.Background(), "full.bin", blipOpener(patternBytes(1, 100_000), 0), quick())
	if !errors.Is(err, syscall.ENOSPC) || temps != 1 {
		t.Fatalf("err %v after %d tries, want ENOSPC after 1", err, temps)
	}
}

func TestCopyInGivesUpOnEndlessWriteFailures(t *testing.T) {
	s, _ := openWritable(t)
	temps := 0
	s.fs = writeFaults{WriteVFS: s.fs.(WriteVFS), temps: &temps, failTemps: 1 << 30, writeErr: errors.New("share went away")}
	opts := quick()
	opts.Attempts = 4
	if _, err := s.CopyIn(context.Background(), "never.bin", blipOpener(patternBytes(1, 100_000), 0), opts); err == nil {
		t.Fatal("copy succeeded")
	}
	if temps != 4 {
		t.Errorf("tried %d times, want 4", temps)
	}
}

func TestCopyInCountsALandedCommitAsDone(t *testing.T) {
	s, _ := openWritable(t)
	temps, lose := 0, true
	s.fs = writeFaults{WriteVFS: s.fs.(WriteVFS), temps: &temps, loseCommit: &lose}
	want := patternBytes(5, 50_000)
	n, err := s.CopyIn(context.Background(), "landed.bin", blipOpener(want, 0), quick())
	if err != nil || n != int64(len(want)) {
		t.Fatalf("n %d, err %v", n, err)
	}
}

func TestCreateClearsStaleTempsOnly(t *testing.T) {
	s, dir := openWritable(t)
	stale := filepath.Join(dir, uploadTempPrefix+"stale")
	fresh := filepath.Join(dir, uploadTempPrefix+"fresh")
	for _, p := range []string{stale, fresh} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * staleTempAge)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("sweep.bin", bytes.NewReader([]byte("hello"))); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stale temp survived: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("a temp that may still be in use was removed: %v", err)
	}
}

// ctxTemps is storage whose temp files can be told to stop.
type ctxTemps struct {
	WriteVFS
	got context.Context
}

func (c *ctxTemps) CreateTempContext(ctx context.Context, dir, pattern string) (io.WriteCloser, string, error) {
	c.got = ctx
	out, p, err := c.WriteVFS.CreateTemp(dir, pattern)
	if err != nil {
		return nil, "", err
	}
	return blockedWriter{out, ctx}, p, nil
}

// blockedWriter is an upload on a link that stands still: a write returns
// only when the copy is cancelled.
type blockedWriter struct {
	io.WriteCloser
	ctx context.Context
}

func (b blockedWriter) Write(p []byte) (int, error) {
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

// A copy's context must reach the storage, or cancelling cannot stop a write
// that is blocked on the network.
func TestCopyInCancelReachesABlockedWrite(t *testing.T) {
	s, dir := openWritable(t)
	store := &ctxTemps{WriteVFS: s.fs.(WriteVFS)}
	s.fs = store
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)

	done := make(chan error, 1)
	go func() {
		_, err := s.CopyIn(ctx, "blocked.bin", blipOpener(patternBytes(3, 100_000), 0), quick())
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("CopyIn returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CopyIn did not return within 2s of being cancelled")
	}
	if store.got == nil {
		t.Error("the storage was never given the copy's context")
	}
	if left := leftoverTemps(t, dir); len(left) != 0 {
		t.Errorf("leftover temp files: %v", left)
	}
}

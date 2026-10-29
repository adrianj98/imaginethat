package xfer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// flaky serves data but fails reads according to fail, which sees the offset
// and how many reads have happened so far.
type flaky struct {
	data  []byte
	fail  func(off int64, call int) error
	calls *int
	opens *int
}

func (f *flaky) opener() Opener {
	return func() (Source, error) {
		*f.opens++
		return flakySrc{f}, nil
	}
}

type flakySrc struct{ f *flaky }

func (s flakySrc) Size() int64  { return int64(len(s.f.data)) }
func (s flakySrc) Close() error { return nil }
func (s flakySrc) ReadAt(p []byte, off int64) (int, error) {
	*s.f.calls++
	if s.f.fail != nil {
		if err := s.f.fail(off, *s.f.calls); err != nil {
			return 0, err
		}
	}
	if off >= int64(len(s.f.data)) {
		return 0, io.EOF
	}
	n := copy(p, s.f.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func newFlaky(size int, fail func(off int64, call int) error) *flaky {
	data := make([]byte, size)
	rand.New(rand.NewSource(1)).Read(data)
	return &flaky{data: data, fail: fail, calls: new(int), opens: new(int)}
}

func fastOpts() Options {
	return Options{ChunkSize: 1000, sleep: func(context.Context, time.Duration) error { return nil }}
}

func TestCopiesCleanSource(t *testing.T) {
	f := newFlaky(10_500, nil)
	var p Progress
	opts := fastOpts()
	opts.Progress = &p
	got, err := io.ReadAll(NewReader(context.Background(), f.opener(), opts))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, f.data) {
		t.Fatal("copy differs from source")
	}
	if done, total, retries := p.Snapshot(); done != 10_500 || total != 10_500 || retries != 0 {
		t.Fatalf("progress = %d/%d retries %d", done, total, retries)
	}
}

func TestResumesAfterTransientErrors(t *testing.T) {
	// Every third read fails: more failures in total than Attempts, but never
	// more than one in a row, so the copy must finish.
	f := newFlaky(50_000, func(off int64, call int) error {
		if call%3 == 0 {
			return errors.New("connection reset")
		}
		return nil
	})
	var p Progress
	opts := fastOpts()
	opts.Progress = &p
	got, err := io.ReadAll(NewReader(context.Background(), f.opener(), opts))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, f.data) {
		t.Fatal("copy differs from source")
	}
	if _, _, retries := p.Snapshot(); retries < 10 {
		t.Fatalf("retries = %d, want the failures to have been retried", retries)
	}
	if *f.opens < 10 {
		t.Fatalf("opens = %d, want a reopen per failure", *f.opens)
	}
}

func TestGivesUpAfterAttempts(t *testing.T) {
	f := newFlaky(5000, func(off int64, call int) error {
		if off >= 2000 {
			return errors.New("EIO")
		}
		return nil
	})
	opts := fastOpts()
	opts.Attempts = 3
	got, err := io.ReadAll(NewReader(context.Background(), f.opener(), opts))
	if err == nil {
		t.Fatal("want an error")
	}
	// io.ReadAll's buffer sizes decide exactly where the last good read ends.
	if len(got) < 2000 || len(got) >= 5000 || !bytes.Equal(got, f.data[:len(got)]) {
		t.Fatalf("delivered %d bytes before failing, want a correct prefix of at least 2000", len(got))
	}
}

func TestPermanentErrorsStopAtOnce(t *testing.T) {
	sentinel := errors.New("wrong password")
	for _, tc := range []struct {
		name string
		err  error
		perm []error
	}{
		{"not exist", &fs.PathError{Op: "read", Path: "x", Err: fs.ErrNotExist}, nil},
		{"permission", fs.ErrPermission, nil},
		{"extra", sentinel, []error{sentinel}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFlaky(5000, func(int64, int) error { return tc.err })
			opts := fastOpts()
			opts.Permanent = tc.perm
			_, err := io.ReadAll(NewReader(context.Background(), f.opener(), opts))
			if !errors.Is(err, tc.err) {
				t.Fatalf("err = %v", err)
			}
			if *f.calls != 1 {
				t.Fatalf("calls = %d, want no retry", *f.calls)
			}
		})
	}
}

func TestSourceChangingSizeFails(t *testing.T) {
	f := newFlaky(5000, nil)
	opens := 0
	open := func() (Source, error) {
		opens++
		if opens > 1 {
			f.data = append(f.data, 1) // grew between opens
		}
		return flakySrc{f}, nil
	}
	f.fail = func(off int64, call int) error {
		if call == 2 {
			return errors.New("blip")
		}
		return nil
	}
	_, err := io.ReadAll(NewReader(context.Background(), open, fastOpts()))
	if !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("err = %v, want ErrSourceChanged", err)
	}
}

func TestTruncatedSourceIsRetriedThenFails(t *testing.T) {
	// ReadAt returns EOF before the size it reported: the read is retried and,
	// if it keeps happening, the copy fails rather than ending short.
	f := newFlaky(5000, func(off int64, call int) error {
		if off >= 3000 {
			return io.EOF
		}
		return nil
	})
	opts := fastOpts()
	opts.Attempts = 2
	_, err := io.ReadAll(NewReader(context.Background(), f.opener(), opts))
	if !errors.Is(err, ErrTruncated) || errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err = %v, want ErrTruncated", err)
	}
}

func TestCancelStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := newFlaky(5000, func(off int64, call int) error {
		cancel()
		return errors.New("blip")
	})
	_, err := io.ReadAll(NewReader(ctx, f.opener(), fastOpts()))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestBackoffDoubles(t *testing.T) {
	var waits []time.Duration
	f := newFlaky(5000, func(int64, int) error { return errors.New("down") })
	opts := Options{Attempts: 5, BaseDelay: time.Second, MaxDelay: 3 * time.Second,
		sleep: func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }}
	io.ReadAll(NewReader(context.Background(), f.opener(), opts))
	want := []time.Duration{time.Second, 2 * time.Second, 3 * time.Second, 3 * time.Second}
	if len(waits) != len(want) {
		t.Fatalf("waits = %v, want %v", waits, want)
	}
	for i := range want {
		if waits[i] != want[i] {
			t.Fatalf("waits = %v, want %v", waits, want)
		}
	}
}

func TestOpenFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.bin")
	data := bytes.Repeat([]byte("imagethat"), 1000)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(NewReader(context.Background(), OpenFile(p), Options{}))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("err = %v, equal = %v", err, bytes.Equal(got, data))
	}
	if _, err := io.ReadAll(NewReader(context.Background(), OpenFile(dir), Options{})); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("directory: err = %v, want fs.ErrInvalid", err)
	}
	if _, err := io.ReadAll(NewReader(context.Background(), OpenFile(p+"x"), Options{})); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing: err = %v, want fs.ErrNotExist", err)
	}
}

func TestOpenIsRetried(t *testing.T) {
	f := newFlaky(3000, nil)
	fails := 2
	open := func() (Source, error) {
		if fails > 0 {
			fails--
			return nil, errors.New("server busy")
		}
		return flakySrc{f}, nil
	}
	r := NewReader(context.Background(), open, fastOpts())
	if size, err := r.Size(); err != nil || size != 3000 {
		t.Fatalf("Size = %d, %v", size, err)
	}
	got, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(got, f.data) {
		t.Fatalf("err %v, equal %v", err, bytes.Equal(got, f.data))
	}
}

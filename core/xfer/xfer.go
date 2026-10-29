// Package xfer copies files so that a flaky source does not cost the whole
// transfer.
//
// A Reader streams a source front to back through ReadAt. When a read fails
// with an error that might go away (an I/O error on a network mount, a reset
// connection, an HTTP 5xx), it closes the source, waits with exponential
// backoff, reopens it and carries on from the last byte it delivered. Errors
// that will not go away (missing file, permission, cancellation, the source
// changing size underneath the copy) stop it at once.
//
// The Reader only reads. Callers write the bytes to a temp file and rename it
// into place once the Reader reports io.EOF, so a failed copy never leaves a
// partial file at the destination (vault.Session.Create and cache.Files both
// work that way).
package xfer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sync/atomic"
	"time"
)

// ErrSourceChanged means the source's size changed between reopens, so the
// bytes already copied may not belong with the rest.
var ErrSourceChanged = errors.New("xfer: source changed during copy")

// ErrTruncated means the source ended before the size it reported. It is
// deliberately not io.ErrUnexpectedEOF: io.ReadFull callers treat that as a
// normal short final read, which would turn a failed copy into a short file.
var ErrTruncated = errors.New("xfer: source ended early")

// Source is one open handle on the file being copied.
type Source interface {
	io.ReaderAt
	io.Closer
	// Size is the file's length when it was opened.
	Size() int64
}

// Opener opens the source. It is called again after each retryable failure.
type Opener func() (Source, error)

// Options tunes retries. The zero value is usable.
type Options struct {
	// Attempts is how many times one stretch of the copy is retried before
	// giving up. The count resets whenever a read makes progress, so a long
	// copy survives any number of separate blips. Default 6.
	Attempts int
	// BaseDelay is the first wait; it doubles up to MaxDelay. Defaults
	// 250ms and 8s.
	BaseDelay, MaxDelay time.Duration
	// ChunkSize caps a single ReadAt. Default 1 MiB.
	ChunkSize int
	// Permanent lists extra errors, matched with errors.Is, that must not be
	// retried, on top of the built-in ones.
	Permanent []error
	// Progress, if set, counts bytes as they are delivered.
	Progress *Progress

	// sleep is replaced in tests.
	sleep func(ctx context.Context, d time.Duration) error
}

func (o *Options) defaults() {
	if o.Attempts <= 0 {
		o.Attempts = 6
	}
	if o.BaseDelay <= 0 {
		o.BaseDelay = 250 * time.Millisecond
	}
	if o.MaxDelay <= 0 {
		o.MaxDelay = 8 * time.Second
	}
	if o.ChunkSize <= 0 {
		o.ChunkSize = 1 << 20
	}
	if o.sleep == nil {
		o.sleep = sleepCtx
	}
}

// Progress is safe to read from another goroutine while a copy runs.
type Progress struct {
	done, total, retries atomic.Int64
}

// Snapshot returns bytes delivered, the source size (0 until known) and how
// many retries have happened so far.
func (p *Progress) Snapshot() (done, total, retries int64) {
	return p.done.Load(), p.total.Load(), p.retries.Load()
}

// Reset clears the counters for the next file of a multi-file job.
func (p *Progress) Reset() {
	p.done.Store(0)
	p.total.Store(0)
	p.retries.Store(0)
}

// Reader is an io.Reader over a source that retries and resumes.
type Reader struct {
	ctx  context.Context
	open Opener
	opts Options

	src  Source
	size int64 // -1 until the first open
	off  int64
}

// NewReader returns a Reader. The source is opened on the first Read.
func NewReader(ctx context.Context, open Opener, opts Options) *Reader {
	opts.defaults()
	return &Reader{ctx: ctx, open: open, opts: opts, size: -1}
}

// Size opens the source if needed and returns its length.
func (r *Reader) Size() (int64, error) {
	if r.size < 0 {
		failures := 0
		if err := r.ensureOpen(&failures); err != nil {
			return 0, err
		}
	}
	return r.size, nil
}

// ensureOpen opens the source if there is no handle, retrying like a read.
func (r *Reader) ensureOpen(failures *int) error {
	for r.src == nil {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		err := r.reopen()
		if err == nil {
			return nil
		}
		if !r.retryable(err) {
			return err
		}
		if err := r.backoff(failures, err); err != nil {
			return err
		}
	}
	return nil
}

// Read implements io.Reader.
func (r *Reader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	failures := 0
	for {
		if err := r.ctx.Err(); err != nil {
			r.Close()
			return 0, err
		}
		if err := r.ensureOpen(&failures); err != nil {
			return 0, err
		}
		if r.off >= r.size {
			r.Close()
			return 0, io.EOF
		}
		buf := p
		if len(buf) > r.opts.ChunkSize {
			buf = buf[:r.opts.ChunkSize]
		}
		if rest := r.size - r.off; int64(len(buf)) > rest {
			buf = buf[:rest]
		}
		n, err := r.src.ReadAt(buf, r.off)
		if n > 0 {
			r.off += int64(n)
			if r.opts.Progress != nil {
				r.opts.Progress.done.Store(r.off)
			}
			// Hand over what arrived; a pending error shows up again on the
			// next read at the new offset if it is real.
			return n, nil
		}
		if err == nil || errors.Is(err, io.EOF) {
			// Nothing came back before the size we opened with: the file got
			// shorter, or the backend dropped the connection mid-read. A
			// reopen tells the two apart.
			err = ErrTruncated
		}
		if !r.retryable(err) {
			r.Close()
			return 0, err
		}
		r.Close()
		if err := r.backoff(&failures, err); err != nil {
			return 0, err
		}
	}
}

// Offset is how many bytes have been delivered.
func (r *Reader) Offset() int64 { return r.off }

// Close releases the current source handle. The Reader can still be read
// afterwards; it reopens the source.
func (r *Reader) Close() error {
	if r.src == nil {
		return nil
	}
	err := r.src.Close()
	r.src = nil
	return err
}

func (r *Reader) reopen() error {
	src, err := r.open()
	if err != nil {
		return err
	}
	size := src.Size()
	if r.size >= 0 && size != r.size {
		src.Close()
		return fmt.Errorf("%w: size %d, was %d", ErrSourceChanged, size, r.size)
	}
	r.src, r.size = src, size
	if r.opts.Progress != nil {
		r.opts.Progress.total.Store(size)
	}
	return nil
}

func (r *Reader) backoff(failures *int, cause error) error {
	*failures++
	if *failures >= r.opts.Attempts {
		return fmt.Errorf("xfer: giving up after %d attempts at byte %d: %w", *failures, r.off, cause)
	}
	if r.opts.Progress != nil {
		r.opts.Progress.retries.Add(1)
	}
	d := r.opts.BaseDelay << (*failures - 1)
	if d > r.opts.MaxDelay || d <= 0 {
		d = r.opts.MaxDelay
	}
	return r.opts.sleep(r.ctx, d)
}

func (r *Reader) retryable(err error) bool {
	return Retryable(err, r.opts.Permanent...)
}

// Retryable reports whether err might succeed on another try. Cancellation,
// missing files, permission problems and a changed source never do; nor does
// anything matching one of extra. A deadline is retryable: a per-request
// timeout is exactly the kind of blip worth another go, and the Reader stops
// on its own context separately.
func Retryable(err error, extra ...error) bool {
	if err == nil {
		return false
	}
	for _, target := range append([]error{
		context.Canceled,
		fs.ErrNotExist, fs.ErrPermission, fs.ErrInvalid,
		ErrSourceChanged,
	}, extra...) {
		if errors.Is(err, target) {
			return false
		}
	}
	return true
}

// OpenFile returns an Opener for a file on the local filesystem (which may be
// a network mount: that is where retries earn their keep).
func OpenFile(path string) Opener {
	return func() (Source, error) {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		info, err := f.Stat()
		if err != nil {
			f.Close()
			return nil, err
		}
		if !info.Mode().IsRegular() {
			f.Close()
			return nil, &fs.PathError{Op: "copy", Path: path, Err: fs.ErrInvalid}
		}
		return fileSource{f, info.Size()}, nil
	}
}

type fileSource struct {
	*os.File
	size int64
}

func (f fileSource) Size() int64 { return f.size }

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

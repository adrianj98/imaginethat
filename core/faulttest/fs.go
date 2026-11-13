package faulttest

import (
	"io"
	"sync"
	"time"

	"github.com/adrianm/imagethat/core/vault"
	"github.com/adrianm/imagethat/core/xfer"
)

// ReadFault makes reads fail the way a network mount does.
type ReadFault struct {
	// From limits the fault to reads that reach this offset or beyond.
	From int64
	// Calls is how many matching reads fail. With For set and Calls zero,
	// every matching read inside the window fails.
	Calls int
	// For is how long matching reads keep failing, from the first one.
	For time.Duration
	// Short returns (0, nil) instead of Err.
	Short bool
	Err   error
}

// Flaky applies a ReadFault and remembers whether it ever fired.
type Flaky struct {
	mu    sync.Mutex
	rf    ReadFault
	set   bool
	used  int
	until time.Time
	fired bool
}

// Set replaces the fault.
func (f *Flaky) Set(rf ReadFault) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rf, f.set, f.used, f.until = rf, true, 0, time.Time{}
}

// Clear ends the fault. Fired keeps its value.
func (f *Flaky) Clear() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.set = false
}

// Fired reports whether a read has failed because of a fault.
func (f *Flaky) Fired() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fired
}

// check reports whether the read of n bytes at off fails, and how.
func (f *Flaky) check(off int64, n int) (fail, short bool, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.set || off+int64(n) <= f.rf.From {
		return false, false, nil
	}
	if f.rf.For > 0 {
		if f.until.IsZero() {
			f.until = time.Now().Add(f.rf.For)
		}
		if time.Now().After(f.until) {
			return false, false, nil
		}
	}
	if f.rf.Calls > 0 {
		if f.used >= f.rf.Calls {
			return false, false, nil
		}
		f.used++
	} else if f.rf.For <= 0 {
		return false, false, nil
	}
	f.fired = true
	return true, f.rf.Short, f.rf.Err
}

// FlakyOpener applies rf to the reads of every Source that open returns.
// The fault's counts carry across reopens.
func FlakyOpener(open xfer.Opener, rf ReadFault) (xfer.Opener, *Flaky) {
	f := &Flaky{}
	f.Set(rf)
	return func() (xfer.Source, error) {
		src, err := open()
		if err != nil {
			return nil, err
		}
		return flakySource{src, f}, nil
	}, f
}

type flakySource struct {
	xfer.Source
	f *Flaky
}

func (s flakySource) ReadAt(p []byte, off int64) (int, error) {
	if fail, short, err := s.f.check(off, len(p)); fail {
		if short {
			return 0, nil
		}
		return 0, err
	}
	return s.Source.ReadAt(p, off)
}

// FS wraps a vault's storage and fails chosen operations. It embeds the
// interface, so the inner backend's optional capabilities (FileOpener,
// StaleTempRemover) are hidden on purpose: every read goes through ReadAt,
// where the faults are.
type FS struct {
	vault.WriteVFS
	reads Flaky

	mu          sync.Mutex
	writeTimes  int
	writeAfter  int64
	writeErr    error
	closeTimes  int
	closeErr    error
	renameTimes int
	renameLands bool
	renameErr   error
	fired       bool
}

// NewFS wraps inner. With nothing armed it behaves like inner.
func NewFS(inner vault.WriteVFS) *FS { return &FS{WriteVFS: inner} }

// FailReads arms rf on ReadAt.
func (f *FS) FailReads(rf ReadFault) { f.reads.Set(rf) }

// FailWrites makes the next times temp files that grow past after bytes fail
// the write that would cross it, and every write after that.
func (f *FS) FailWrites(times int, after int64, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writeTimes, f.writeAfter, f.writeErr = times, after, err
}

// FailClose makes the next times temp files fail Close.
func (f *FS) FailClose(times int, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeTimes, f.closeErr = times, err
}

// FailRename makes the next times renames return err. With landed, the
// rename happens first: the commit went through and its reply was lost.
func (f *FS) FailRename(times int, landed bool, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renameTimes, f.renameLands, f.renameErr = times, landed, err
}

// Clear ends every fault. Fired keeps its value.
func (f *FS) Clear() {
	f.reads.Clear()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writeTimes, f.closeTimes, f.renameTimes = 0, 0, 0
}

// Fired reports whether any fault has returned its error.
func (f *FS) Fired() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fired || f.reads.Fired()
}

func (f *FS) ReadAt(path string, off int64, buf []byte) (int, error) {
	if fail, short, err := f.reads.check(off, len(buf)); fail {
		if short {
			return 0, nil
		}
		return 0, err
	}
	return f.WriteVFS.ReadAt(path, off, buf)
}

func (f *FS) Rename(oldPath, newPath string) error {
	f.mu.Lock()
	fail := f.renameTimes > 0
	lands, err := f.renameLands, f.renameErr
	if fail {
		f.renameTimes--
		f.fired = true
	}
	f.mu.Unlock()
	if !fail {
		return f.WriteVFS.Rename(oldPath, newPath)
	}
	if lands {
		if realErr := f.WriteVFS.Rename(oldPath, newPath); realErr != nil {
			return realErr
		}
	}
	return err
}

func (f *FS) CreateTemp(dir, pattern string) (io.WriteCloser, string, error) {
	w, p, err := f.WriteVFS.CreateTemp(dir, pattern)
	if err != nil {
		return nil, "", err
	}
	return &faultyWriter{WriteCloser: w, fs: f}, p, nil
}

type faultyWriter struct {
	io.WriteCloser
	fs      *FS
	written int64
	failed  error
}

func (w *faultyWriter) Write(p []byte) (int, error) {
	if w.failed != nil {
		return 0, w.failed
	}
	f := w.fs
	f.mu.Lock()
	if f.writeTimes > 0 && w.written+int64(len(p)) > f.writeAfter {
		f.writeTimes--
		f.fired = true
		w.failed = f.writeErr
	}
	f.mu.Unlock()
	if w.failed != nil {
		return 0, w.failed
	}
	n, err := w.WriteCloser.Write(p)
	w.written += int64(n)
	return n, err
}

func (w *faultyWriter) Close() error {
	f := w.fs
	f.mu.Lock()
	fail := f.closeTimes > 0
	err := f.closeErr
	if fail {
		f.closeTimes--
		f.fired = true
	}
	f.mu.Unlock()
	realErr := w.WriteCloser.Close()
	if fail {
		return err
	}
	return realErr
}

// Package cachefs is the cache layer between the vault engine and a network
// transport. Every transport that reaches files over a network (an
// imagethat-web server, an SMB or NFS mount) is wrapped the same way: what is
// read is kept on disk in the shared read-through cache (cache.Through over
// one cache.Ranges store, which the Drive transport uses too), Cache File pins
// a whole file, and what is held stays readable when the network is not.
//
// A transport only says how to name a file and how to tell its versions
// apart (Source.Stat); the rest is here.
package cachefs

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"sync"

	"github.com/adrianm/imagethat/core/cache"
	"github.com/adrianm/imagethat/core/vault"
	"github.com/adrianm/imagethat/core/xfer"
)

// Source is a transport as the cache layer needs it: plain reads, plus Stat.
type Source interface {
	vault.VFS
	// Stat asks the transport for a file's current version tag and size. The
	// tag must change whenever the contents do. It is called when a file is
	// opened (FileSize), never per read.
	Stat(path string) (version string, size int64, err error)
}

// FS is a Source behind the read-through cache.
type FS struct {
	src   Source
	cache *cache.Through
	// prefix keys entries by transport and vault, so one store serves all.
	prefix string
	// fatal are Stat errors that must reach the caller even when the file
	// is held (a pinned certificate that no longer matches, a locked vault);
	// other errors mean "unreachable", and what is held is used instead.
	fatal []error

	mu    sync.Mutex
	metas map[string]cache.Meta // by ciphertext path, as of the last FileSize
}

var (
	_ vault.VFS           = (*FS)(nil)
	_ vault.Cacher        = (*FS)(nil)
	_ vault.CacheReporter = (*FS)(nil)
)

// New wraps src. ranges is the shared store (nil: reads go straight to src).
func New(src Source, ranges *cache.Ranges, prefix string, fatal ...error) *FS {
	return &FS{src: src, cache: cache.NewThrough(ranges), prefix: prefix, fatal: fatal, metas: make(map[string]cache.Meta)}
}

func (f *FS) key(path string) string { return f.prefix + path }

// FileSize asks the transport for the file's version and size, so a file
// changed elsewhere is never served from what is held for an older version.
func (f *FS) FileSize(path string) (int64, error) {
	key := f.key(path)
	version, size, err := f.src.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			f.forget(path)
			return 0, err
		}
		for _, e := range f.fatal {
			if errors.Is(err, e) {
				return 0, err
			}
		}
		// Offline, timed out, or the transport is failing: use what is held.
		if m, ok := f.held(path); ok {
			return m.Size, nil
		}
		return 0, err
	}
	m := cache.Meta{Key: key, Version: version, Size: size}
	f.mu.Lock()
	f.metas[path] = m
	f.mu.Unlock()
	return size, nil
}

// meta is path's cache identity: the one FileSize saw, else a fresh FileSize,
// else (offline) what the store last held for it.
func (f *FS) meta(path string) (cache.Meta, error) {
	f.mu.Lock()
	m, ok := f.metas[path]
	f.mu.Unlock()
	if ok {
		return m, nil
	}
	if _, err := f.FileSize(path); err != nil {
		return cache.Meta{}, err
	}
	f.mu.Lock()
	m, ok = f.metas[path]
	f.mu.Unlock()
	if ok {
		return m, nil
	}
	if m, ok := f.held(path); ok {
		return m, nil
	}
	return cache.Meta{}, fs.ErrNotExist
}

// held is the identity of what the store holds for path, without asking the
// transport.
func (f *FS) held(path string) (cache.Meta, bool) {
	if f.cache.Ranges == nil {
		return cache.Meta{}, false
	}
	key := f.key(path)
	version, total, ok := f.cache.Ranges.Stored(key)
	return cache.Meta{Key: key, Version: version, Size: total}, ok
}

// forget drops path's version and everything held for it.
func (f *FS) forget(path string) {
	f.mu.Lock()
	delete(f.metas, path)
	f.mu.Unlock()
	f.cache.Drop(f.key(path))
}

// ReadAt serves a range from the read-through cache.
func (f *FS) ReadAt(path string, off int64, buf []byte) (int, error) {
	m, err := f.meta(path)
	if err != nil {
		return f.src.ReadAt(path, off, buf)
	}
	return f.cache.ReadAt(m, off, buf, func(_ context.Context, off, n int64) ([]byte, error) {
		data := make([]byte, n)
		k, err := f.src.ReadAt(path, off, data)
		if k > 0 && (err == nil || errors.Is(err, io.EOF)) {
			return data[:k], nil
		}
		return nil, err
	})
}

func (f *FS) ReadDir(path string) ([]vault.DirEntry, error) { return f.src.ReadDir(path) }

func (f *FS) ReadFile(path string) ([]byte, error) { return f.src.ReadFile(path) }

// Cache reads the whole file through into the cache and pins it, so it opens
// without the network and is not evicted.
func (f *FS) Cache(ctx context.Context, path string, opts xfer.Options) error {
	if f.cache.Ranges == nil {
		return cache.ErrNoCache
	}
	r := xfer.NewReader(ctx, vault.RawOpener(f, path), opts)
	defer r.Close()
	// Opening runs FileSize, with retries: that checks the file against the
	// transport and records its version.
	if _, err := r.Size(); err != nil {
		return err
	}
	m, err := f.meta(path)
	if err != nil {
		return err
	}
	return f.cache.Pin(m, func() error {
		_, err := io.Copy(io.Discard, r)
		return err
	})
}

// IsCached reports whether path is held whole on disk. It does not ask the
// transport whether that copy is current; FileSize does that on open.
func (f *FS) IsCached(path string) bool {
	m, ok := f.held(path)
	return ok && f.cache.Complete(m)
}

// Uncache drops everything held for path.
func (f *FS) Uncache(path string) error {
	f.forget(path)
	return nil
}

// CachedFractions reports which parts of path are on disk, for a video
// scrubber's "cached" strip.
func (f *FS) CachedFractions(path string) [][2]float64 {
	m, ok := f.held(path)
	if !ok {
		return nil
	}
	return f.cache.Fractions(m)
}

// WriteFS is FS over a writable transport. Writes go straight through, and
// drop what is held for the paths they touch.
type WriteFS struct {
	*FS
	w vault.WriteVFS
}

var _ vault.WriteVFS = (*WriteFS)(nil)

// NewWrite wraps a writable src (see New).
func NewWrite(src interface {
	Source
	vault.WriteVFS
}, ranges *cache.Ranges, prefix string, fatal ...error) *WriteFS {
	return &WriteFS{FS: New(src, ranges, prefix, fatal...), w: src}
}

func (f *WriteFS) Mkdir(path string) error { return f.w.Mkdir(path) }

func (f *WriteFS) Remove(path string) error {
	f.forget(path)
	return f.w.Remove(path)
}

func (f *WriteFS) RemoveAll(path string) error {
	// Children are keyed by their own paths; they are dropped when next
	// opened (their Stat fails) or evicted.
	f.forget(path)
	return f.w.RemoveAll(path)
}

func (f *WriteFS) Rename(oldPath, newPath string) error {
	f.forget(oldPath)
	f.forget(newPath)
	return f.w.Rename(oldPath, newPath)
}

func (f *WriteFS) WriteFile(path string, data []byte) error {
	f.forget(path)
	return f.w.WriteFile(path, data)
}

func (f *WriteFS) CreateTemp(dir, pattern string) (io.WriteCloser, string, error) {
	return f.w.CreateTemp(dir, pattern)
}

func (f *WriteFS) Exists(path string) bool { return f.w.Exists(path) }

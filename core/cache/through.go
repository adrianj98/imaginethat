package cache

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
)

// Meta identifies one version of a remote file for the read-through cache.
// Key names the file across sessions (a Drive file ID, a server path); Version
// changes whenever the file's contents do (Drive's modifiedTime, a gocryptfs
// header), so a file changed elsewhere is never served stale.
type Meta struct {
	Key     string
	Version string
	Size    int64
}

// Fetch reads n bytes at off straight from the transport.
type Fetch func(ctx context.Context, off, n int64) ([]byte, error)

// Through puts a Ranges store behind a transport's reads, the same way for
// every network backend: whatever is read is kept on disk, as ciphertext, and
// read from disk the next time. A small file is fetched whole on first read, a
// large one in Window-sized pieces, because the vault engine reads one 4 KiB
// block at a time and a round trip per block would make a video unwatchable.
type Through struct {
	Ranges *Ranges

	mu      sync.Mutex
	windows map[string]chan struct{} // fetches in progress, by key + window
}

// Read sizes. Files up to WholeFileMax are downloaded in one request on first
// read; larger ones in Window pieces.
var (
	WholeFileMax int64 = 50 << 20
	Window       int64 = 8 << 20
)

// NewThrough wraps r, which may be nil (then every read goes to the transport).
func NewThrough(r *Ranges) *Through {
	return &Through{Ranges: r, windows: make(map[string]chan struct{})}
}

// ReadAt serves a range from the disk cache or fetches the window around it.
func (t *Through) ReadAt(m Meta, off int64, buf []byte, fetch Fetch) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	if off >= m.Size {
		return 0, io.EOF
	}
	want := int64(len(buf))
	if off+want > m.Size {
		want = m.Size - off
	}
	if t == nil || t.Ranges == nil {
		data, err := fetch(context.Background(), off, want)
		if err != nil {
			return 0, err
		}
		return served(copy(buf, data), len(buf))
	}
	for {
		if n, ok := t.Ranges.ReadAt(m.Key, m.Version, off, buf[:want]); ok {
			return served(n, len(buf))
		}
		start, end := window(m.Size, off, want)
		key := fmt.Sprintf("%s@%d", m.Key, start)
		t.mu.Lock()
		if ch, busy := t.windows[key]; busy {
			t.mu.Unlock()
			<-ch // another reader is fetching this window; then try the cache again
			continue
		}
		ch := make(chan struct{})
		t.windows[key] = ch
		t.mu.Unlock()

		data, err := fetch(context.Background(), start, end-start)
		if err == nil && len(data) > 0 {
			err = t.Ranges.Write(m.Key, m.Version, m.Size, start, data)
		}

		t.mu.Lock()
		delete(t.windows, key)
		close(ch)
		t.mu.Unlock()

		if err != nil && len(data) == 0 {
			return 0, err
		}
		if int64(len(data)) < off-start+1 {
			return 0, io.EOF
		}
		// Serve from what was fetched even if the cache write failed (disk full).
		n := copy(buf[:want], data[off-start:])
		return served(n, len(buf))
	}
}

func served(n, asked int) (int, error) {
	if n < asked {
		return n, io.EOF
	}
	return n, nil
}

// window is the byte range to fetch for a read of want bytes at off.
func window(size, off, want int64) (int64, int64) {
	if size <= WholeFileMax {
		return 0, size
	}
	start := off / Window * Window
	end := start + Window
	if end < off+want {
		end = off + want
	}
	if end > size {
		end = size
	}
	return start, end
}

// ErrNoCache is returned by Pin when there is no disk cache to keep files in.
var ErrNoCache = errors.New("cache: no file cache configured")

// Pin keeps m on disk for good: read reads the whole file through (it should
// go via ReadAt, so the windows land in the cache), and the entry is pinned so
// it is not evicted. Pinning comes first, so windows already fetched stay put
// while the rest arrives.
func (t *Through) Pin(m Meta, read func() error) error {
	if t == nil || t.Ranges == nil {
		return ErrNoCache
	}
	t.Ranges.Pin(m.Key, true)
	if !t.Ranges.Complete(m.Key, m.Version) {
		if err := read(); err != nil {
			t.Ranges.Pin(m.Key, false)
			return err
		}
	}
	if !t.Ranges.Complete(m.Key, m.Version) {
		return errors.New("cache: file did not fit in the cache")
	}
	return nil
}

// Complete reports whether the whole of m is on disk.
func (t *Through) Complete(m Meta) bool {
	return t != nil && t.Ranges != nil && t.Ranges.Complete(m.Key, m.Version)
}

// Drop forgets everything held for key.
func (t *Through) Drop(key string) {
	if t != nil && t.Ranges != nil {
		t.Ranges.Remove(key)
	}
}

// Fractions reports which parts of m are on disk, as [start, end) fractions
// of its length, for a "buffered" strip on a video scrubber. Ciphertext and
// plaintext offsets differ only by gocryptfs' fixed per-block overhead, so the
// fractions carry over to the decrypted file.
func (t *Through) Fractions(m Meta) [][2]float64 {
	if t == nil || t.Ranges == nil || m.Size <= 0 {
		return nil
	}
	var out [][2]float64
	for _, iv := range t.Ranges.Intervals(m.Key, m.Version) {
		out = append(out, [2]float64{float64(iv.Start) / float64(m.Size), float64(iv.End) / float64(m.Size)})
	}
	return out
}

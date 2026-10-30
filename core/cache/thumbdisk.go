package cache

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Sealer seals and opens bytes under a key that lives only while a vault is
// unlocked. *vault.Session is the implementation; the indirection keeps this
// package free of the vault import.
type Sealer interface {
	SealBlob(plaintext []byte) ([]byte, error)
	OpenBlob(ciphertext []byte) ([]byte, error)
}

// ThumbDisk persists previews across launches, each sealed with the vault's
// own content key, so the cache is exactly as readable as the vault: not at
// all without the password. It is the Go port of the macOS app's
// PersistentThumbnailCache, which stored entries the same way.
//
// Layout: <root>/<sha256(sourceID)[:16 bytes]>/<sha256(key)>.thumb. One
// directory per source lets a whole vault's previews be dropped at once. An
// entry is magic, a version byte and the sealed JPEG; anything that does not
// parse is deleted on read.
//
// The budget is enforced after writes by evicting the least recently used
// entries (mtime, refreshed on hit) until the total fits.
type ThumbDisk struct {
	root   string
	budget int64

	mu    sync.Mutex
	total int64 // bytes on disk; -1 until first measured
}

var thumbMagic = []byte{'I', 'T', 'H', 'M', 1}

// NewThumbDisk returns a store rooted at root holding at most budget bytes.
// The directory is created on first write.
func NewThumbDisk(root string, budget int64) *ThumbDisk {
	return &ThumbDisk{root: root, budget: budget, total: -1}
}

var (
	thumbDisksMu sync.Mutex
	thumbDisks   = make(map[string]*ThumbDisk)
)

// SharedThumbDisk returns the one store for root, so every session previewing
// into the same directory shares its budget accounting. budget <= 0 means
// 1 GiB for a new store and leaves an existing store's budget alone.
func SharedThumbDisk(root string, budget int64) *ThumbDisk {
	thumbDisksMu.Lock()
	defer thumbDisksMu.Unlock()
	if d, ok := thumbDisks[root]; ok {
		if budget > 0 {
			d.mu.Lock()
			d.budget = budget
			d.mu.Unlock()
		}
		return d
	}
	if budget <= 0 {
		budget = 1 << 30
	}
	d := NewThumbDisk(root, budget)
	thumbDisks[root] = d
	return d
}

// Root is the directory the store writes under.
func (d *ThumbDisk) Root() string { return d.root }

func (d *ThumbDisk) sourceDir(sourceID string) string {
	h := sha256.Sum256([]byte(sourceID))
	return filepath.Join(d.root, hex.EncodeToString(h[:16]))
}

func (d *ThumbDisk) entryPath(sourceID, key string) string {
	h := sha256.Sum256([]byte(key))
	return filepath.Join(d.sourceDir(sourceID), hex.EncodeToString(h[:])+".thumb")
}

// Get returns the stored preview for key, or false. A corrupt or
// undecryptable entry is removed.
func (d *ThumbDisk) Get(sourceID, key string, s Sealer) ([]byte, bool) {
	p := d.entryPath(sourceID, key)
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, false
	}
	if !bytes.HasPrefix(raw, thumbMagic) {
		d.remove(p, int64(len(raw)))
		return nil, false
	}
	jpeg, err := s.OpenBlob(raw[len(thumbMagic):])
	if err != nil {
		d.remove(p, int64(len(raw)))
		return nil, false
	}
	// Refresh for LRU; a failure here only affects eviction order.
	now := time.Now()
	_ = os.Chtimes(p, now, now)
	return jpeg, true
}

// Put stores jpeg under key. The write goes to a temp file first so a reader
// never sees a partial entry.
func (d *ThumbDisk) Put(sourceID, key string, s Sealer, jpeg []byte) error {
	sealed, err := s.SealBlob(jpeg)
	if err != nil {
		return err
	}
	if int64(len(sealed)) > d.budget {
		return nil // never worth evicting everything else for one entry
	}
	dir := d.sourceDir(sourceID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	p := d.entryPath(sourceID, key)

	var old int64
	if fi, err := os.Stat(p); err == nil {
		old = fi.Size()
	}

	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_, werr := tmp.Write(thumbMagic)
	if werr == nil {
		_, werr = tmp.Write(sealed)
	}
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmpName)
		return errors.Join(werr, cerr)
	}
	if err := os.Rename(tmpName, p); err != nil {
		_ = os.Remove(tmpName)
		return err
	}

	d.mu.Lock()
	if d.total >= 0 {
		d.total += int64(len(thumbMagic)+len(sealed)) - old
	}
	d.mu.Unlock()
	d.evictIfNeeded()
	return nil
}

// Invalidate drops one entry.
func (d *ThumbDisk) Invalidate(sourceID, key string) {
	p := d.entryPath(sourceID, key)
	if fi, err := os.Stat(p); err == nil {
		d.remove(p, fi.Size())
	}
}

// ClearSource drops every entry of one source.
func (d *ThumbDisk) ClearSource(sourceID string) error {
	err := os.RemoveAll(d.sourceDir(sourceID))
	d.mu.Lock()
	d.total = -1
	d.mu.Unlock()
	return err
}

// Clear drops every entry of every source.
func (d *ThumbDisk) Clear() error {
	entries, err := os.ReadDir(d.root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	var firstErr error
	for _, e := range entries {
		if e.IsDir() && len(e.Name()) == 32 {
			if err := os.RemoveAll(filepath.Join(d.root, e.Name())); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	d.mu.Lock()
	d.total = 0
	d.mu.Unlock()
	return firstErr
}

// Size returns the bytes currently stored.
func (d *ThumbDisk) Size() int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.total < 0 {
		d.total = d.measureLocked()
	}
	return d.total
}

func (d *ThumbDisk) remove(p string, size int64) {
	if err := os.Remove(p); err == nil {
		d.mu.Lock()
		if d.total >= 0 {
			d.total -= size
		}
		d.mu.Unlock()
	}
}

type diskEntry struct {
	path  string
	size  int64
	mtime time.Time
}

// entries lists every stored preview.
func (d *ThumbDisk) entries() []diskEntry {
	var out []diskEntry
	_ = filepath.WalkDir(d.root, func(p string, de fs.DirEntry, err error) error {
		if err != nil || de.IsDir() || filepath.Ext(p) != ".thumb" {
			return nil
		}
		if fi, err := de.Info(); err == nil {
			out = append(out, diskEntry{path: p, size: fi.Size(), mtime: fi.ModTime()})
		}
		return nil
	})
	return out
}

func (d *ThumbDisk) measureLocked() int64 {
	var total int64
	for _, e := range d.entries() {
		total += e.size
	}
	return total
}

func (d *ThumbDisk) evictIfNeeded() {
	d.mu.Lock()
	if d.total < 0 {
		d.total = d.measureLocked()
	}
	if d.total <= d.budget {
		d.mu.Unlock()
		return
	}
	d.mu.Unlock()

	es := d.entries()
	sort.Slice(es, func(i, j int) bool { return es[i].mtime.Before(es[j].mtime) })
	var total int64
	for _, e := range es {
		total += e.size
	}
	for _, e := range es {
		if total <= d.budget {
			break
		}
		if err := os.Remove(e.path); err == nil {
			total -= e.size
		}
	}
	d.mu.Lock()
	d.total = total
	d.mu.Unlock()
}

// ThumbKey builds the cache key the thumbnail service uses: the path, the
// requested size and the file's plaintext size, so an overwritten file never
// serves a stale preview.
func ThumbKey(path string, maxEdge int, plainSize int64) string {
	var sz [8]byte
	binary.BigEndian.PutUint64(sz[:], uint64(plainSize))
	return fmt.Sprintf("%s|%d|%x", path, maxEdge, sz)
}

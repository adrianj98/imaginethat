package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Ranges keeps byte ranges of remote files on disk, for backends that read
// files in pieces (Google Drive). Each file is one sparse file on disk plus a
// list of the intervals it holds. Only ciphertext goes in here, exactly as the
// remote has it; callers never store decrypted bytes.
//
// An entry belongs to one version of the file (Drive's modifiedTime): asking
// with any other version drops the entry, so a file changed elsewhere is never
// served stale. Entries are evicted least recently used first once the total
// passes the budget, except pinned ones: those were kept on purpose (Cache
// File) and leave only when unpinned or removed.
type Ranges struct {
	dir string

	mu     sync.Mutex
	budget int64
	index  map[string]*rangeEntry
	// pins holds Pin calls made before the entry has any data, so a file
	// being cached is protected from its first window on.
	pins map[string]bool
}

// Interval is a half-open byte range [Start, End).
type Interval struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

type rangeEntry struct {
	Key       string     `json:"key"`
	Version   string     `json:"version"`
	Total     int64      `json:"total"`
	Intervals []Interval `json:"intervals"`
	Pinned    bool       `json:"pinned,omitempty"`
	LastUsed  time.Time  `json:"lastUsed"`
}

func (e *rangeEntry) held() int64 {
	var n int64
	for _, iv := range e.Intervals {
		n += iv.End - iv.Start
	}
	return n
}

func (e *rangeEntry) covers(off, end int64) bool {
	for _, iv := range e.Intervals {
		if iv.Start <= off && iv.End >= end {
			return true
		}
	}
	return false
}

const rangesIndex = "index.json"

// DefaultRangesBudget bounds a Ranges store when the caller passes 0.
const DefaultRangesBudget = 20 << 30

var (
	rangesMu    sync.Mutex
	rangeStores = make(map[string]*Ranges)
)

// SharedRanges returns the store for dir, creating it on first use. Every
// session reading through the same directory shares one store, so their
// budgets and index stay consistent. budget <= 0 keeps the current budget (or
// DefaultRangesBudget for a new store).
func SharedRanges(dir string, budget int64) (*Ranges, error) {
	rangesMu.Lock()
	defer rangesMu.Unlock()
	if r, ok := rangeStores[dir]; ok {
		if budget > 0 {
			r.SetBudget(budget)
		}
		return r, nil
	}
	r, err := NewRanges(dir, budget)
	if err != nil {
		return nil, err
	}
	rangeStores[dir] = r
	return r, nil
}

// NewRanges opens (or creates) a store in dir. Prefer SharedRanges, which
// keeps one store per directory.
func NewRanges(dir string, budget int64) (*Ranges, error) {
	if dir == "" {
		return nil, errors.New("cache: empty ranges directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if budget <= 0 {
		budget = DefaultRangesBudget
	}
	r := &Ranges{dir: dir, budget: budget, index: make(map[string]*rangeEntry), pins: make(map[string]bool)}
	if data, err := os.ReadFile(filepath.Join(dir, rangesIndex)); err == nil {
		var entries []*rangeEntry
		if json.Unmarshal(data, &entries) == nil {
			for _, e := range entries {
				if _, err := os.Stat(r.path(e.Key)); err == nil {
					r.index[e.Key] = e
				}
			}
		}
	}
	return r, nil
}

func (r *Ranges) path(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(r.dir, hex.EncodeToString(sum[:]))
}

// SetBudget changes the size limit and evicts down to it.
func (r *Ranges) SetBudget(budget int64) {
	if budget <= 0 {
		return
	}
	r.mu.Lock()
	r.budget = budget
	r.evictLocked()
	r.saveLocked()
	r.mu.Unlock()
}

// entryLocked returns key's entry for version, dropping one held for a
// different version.
func (r *Ranges) entryLocked(key, version string) *rangeEntry {
	e, ok := r.index[key]
	if !ok {
		return nil
	}
	if e.Version != version {
		r.removeLocked(key)
		return nil
	}
	return e
}

// ReadAt fills buf from offset off when the store holds all of it, and
// reports whether it did. A request running past the end of the file is
// served up to the end; n tells how much.
func (r *Ranges) ReadAt(key, version string, off int64, buf []byte) (n int, ok bool) {
	r.mu.Lock()
	e := r.entryLocked(key, version)
	if e == nil {
		r.mu.Unlock()
		return 0, false
	}
	end := off + int64(len(buf))
	if end > e.Total {
		end = e.Total
	}
	if off >= end || !e.covers(off, end) {
		r.mu.Unlock()
		return 0, false
	}
	e.LastUsed = time.Now()
	r.mu.Unlock()

	f, err := os.Open(r.path(key))
	if err != nil {
		r.Remove(key)
		return 0, false
	}
	defer f.Close()
	n, err = f.ReadAt(buf[:end-off], off)
	if err != nil && !errors.Is(err, io.EOF) {
		return 0, false
	}
	return n, n == int(end-off)
}

// Write stores data at offset off of key's file, whose full length is total.
func (r *Ranges) Write(key, version string, total, off int64, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.entryLocked(key, version)
	if e != nil && e.Total != total {
		r.removeLocked(key)
		e = nil
	}
	f, err := os.OpenFile(r.path(key), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	if e == nil {
		// A fresh entry starts from an empty file, never from leftovers.
		if err := f.Truncate(0); err != nil {
			f.Close()
			return err
		}
	}
	if _, err := f.WriteAt(data, off); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if e == nil {
		e = &rangeEntry{Key: key, Version: version, Total: total, Pinned: r.pins[key]}
		r.index[key] = e
	}
	e.Intervals = mergeIntervals(append(e.Intervals, Interval{off, off + int64(len(data))}))
	e.LastUsed = time.Now()
	r.evictLocked()
	r.saveLocked()
	return nil
}

func mergeIntervals(in []Interval) []Interval {
	sort.Slice(in, func(i, j int) bool { return in[i].Start < in[j].Start })
	out := in[:0]
	for _, iv := range in {
		if n := len(out); n > 0 && iv.Start <= out[n-1].End {
			if iv.End > out[n-1].End {
				out[n-1].End = iv.End
			}
			continue
		}
		out = append(out, iv)
	}
	return out
}

// Intervals returns the ranges held for key at version.
func (r *Ranges) Intervals(key, version string) []Interval {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.entryLocked(key, version)
	if e == nil {
		return nil
	}
	return append([]Interval(nil), e.Intervals...)
}

// Stored returns the version and total size the entry for key was written
// with, so a caller that cannot reach the remote can still use what is held.
func (r *Ranges) Stored(key string) (version string, total int64, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.index[key]
	if !ok {
		return "", 0, false
	}
	return e.Version, e.Total, true
}

// Complete reports whether the whole file is held.
func (r *Ranges) Complete(key, version string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.entryLocked(key, version)
	return e != nil && (e.Total == 0 || e.covers(0, e.Total))
}

// Pin keeps key out of eviction (or lets it be evicted again). It may be
// called before anything is stored for key.
func (r *Ranges) Pin(key string, pinned bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if pinned {
		r.pins[key] = true
	} else {
		delete(r.pins, key)
	}
	if e, ok := r.index[key]; ok && e.Pinned != pinned {
		e.Pinned = pinned
		r.evictLocked()
		r.saveLocked()
	}
}

// Pinned reports whether key is pinned.
func (r *Ranges) Pinned(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.index[key]
	return ok && e.Pinned
}

// Remove drops everything held for key.
func (r *Ranges) Remove(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.removeLocked(key)
	r.saveLocked()
}

func (r *Ranges) removeLocked(key string) {
	delete(r.index, key)
	delete(r.pins, key)
	_ = os.Remove(r.path(key))
}

// Size is the number of bytes held.
func (r *Ranges) Size() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	var n int64
	for _, e := range r.index {
		n += e.held()
	}
	return n
}

// Clear drops every entry, pinned ones included.
func (r *Ranges) Clear() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key := range r.index {
		r.removeLocked(key)
	}
	r.saveLocked()
	return nil
}

func (r *Ranges) evictLocked() {
	var total int64
	var victims []*rangeEntry
	for _, e := range r.index {
		total += e.held()
		if !e.Pinned {
			victims = append(victims, e)
		}
	}
	if total <= r.budget {
		return
	}
	sort.Slice(victims, func(i, j int) bool { return victims[i].LastUsed.Before(victims[j].LastUsed) })
	for _, e := range victims {
		if total <= r.budget {
			break
		}
		total -= e.held()
		r.removeLocked(e.Key)
	}
}

// saveLocked writes the index. It runs after every change that matters for
// what is on disk; last-used times alone are saved with the next such change.
func (r *Ranges) saveLocked() {
	entries := make([]*rangeEntry, 0, len(r.index))
	for _, e := range r.index {
		entries = append(entries, e)
	}
	data, err := json.Marshal(entries)
	if err != nil {
		return
	}
	tmp := filepath.Join(r.dir, rangesIndex+".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, filepath.Join(r.dir, rangesIndex)); err != nil {
		_ = os.Remove(tmp)
	}
}

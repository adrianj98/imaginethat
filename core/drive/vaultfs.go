package drive

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"path"
	"strings"
	"sync"

	"github.com/adrianm/imagethat/core/cache"
	"github.com/adrianm/imagethat/core/vault"
	"github.com/adrianm/imagethat/core/xfer"
)

// VaultFS is a gocryptfs vault stored in a Drive folder, as a vault.WriteVFS.
// Paths are vault-relative ciphertext paths; the root folder is "".
//
// Drive addresses files by ID, so VaultFS keeps an index from ciphertext
// paths to IDs, filled one directory listing at a time as the vault engine
// walks the tree, and updated by its own writes. Changes made elsewhere while
// the vault is open show up after Refresh or the next open.
//
// Reads go through the shared read-through cache (cache.Through) when a
// cache.Ranges is given, the same layer every network transport uses. Cache
// entries are keyed by file ID and tied to Drive's modifiedTime, so a file
// changed elsewhere is fetched again.
type VaultFS struct {
	c      *Client
	rootID string
	cache  *cache.Through

	mu       sync.Mutex
	folders  map[string]string // cipher dir -> folder ID
	files    map[string]File   // cipher file -> metadata
	listed   map[string]bool   // dirs whose children are in the index
	inflight map[string]chan struct{}
}

var (
	_ vault.WriteVFS = (*VaultFS)(nil)
	_ vault.Cacher   = (*VaultFS)(nil)
)

// NewVaultFS opens the vault folder rootID. ranges may be nil.
func NewVaultFS(c *Client, rootID string, ranges *cache.Ranges) *VaultFS {
	return &VaultFS{
		c:        c,
		rootID:   rootID,
		cache:    cache.NewThrough(ranges),
		folders:  map[string]string{"": rootID},
		files:    make(map[string]File),
		listed:   make(map[string]bool),
		inflight: make(map[string]chan struct{}),
	}
}

// OpenVault unlocks the gocryptfs vault in the Drive folder rootID. ranges
// may be nil (no disk cache).
func OpenVault(c *Client, rootID, password string, ranges *cache.Ranges) (*vault.Session, *VaultFS, error) {
	v := NewVaultFS(c, rootID, ranges)
	s, err := vault.Open(v, password)
	if err != nil {
		return nil, nil, err
	}
	return s, v, nil
}

// InitVault creates a new empty vault in the (empty) Drive folder rootID.
func InitVault(c *Client, rootID, password string) error {
	return vault.Init(NewVaultFS(c, rootID, nil), password)
}

func parentOf(p string) string {
	d := path.Dir(p)
	if d == "." || d == "/" {
		return ""
	}
	return d
}

func notExist(op, p string) error { return &fs.PathError{Op: op, Path: p, Err: fs.ErrNotExist} }

// ensureListed puts dir's children in the index. One listing per directory
// runs at a time; others wait for it. A failed listing is not remembered.
func (v *VaultFS) ensureListed(dir string) error {
	for {
		v.mu.Lock()
		if v.listed[dir] {
			v.mu.Unlock()
			return nil
		}
		if ch, ok := v.inflight[dir]; ok {
			v.mu.Unlock()
			<-ch
			continue
		}
		id, ok := v.folders[dir]
		if !ok {
			v.mu.Unlock()
			if dir == "" {
				return notExist("readdir", dir)
			}
			// The parent may not be listed yet.
			if err := v.ensureListed(parentOf(dir)); err != nil {
				return err
			}
			v.mu.Lock()
			_, ok = v.folders[dir]
			v.mu.Unlock()
			if !ok {
				return notExist("readdir", dir)
			}
			continue
		}
		ch := make(chan struct{})
		v.inflight[dir] = ch
		v.mu.Unlock()

		entries, err := v.c.List(context.Background(), id)

		v.mu.Lock()
		delete(v.inflight, dir)
		close(ch)
		if err == nil {
			v.listed[dir] = true
			for _, e := range entries {
				child := path.Join(dir, e.Name)
				if e.IsFolder {
					v.folders[child] = e.ID
				} else {
					v.files[child] = e
				}
			}
		}
		v.mu.Unlock()
		return err
	}
}

func (v *VaultFS) file(p string) (File, error) {
	if err := v.ensureListed(parentOf(p)); err != nil {
		return File{}, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	f, ok := v.files[p]
	if !ok {
		return File{}, notExist("stat", p)
	}
	return f, nil
}

func (v *VaultFS) folder(p string) (string, error) {
	if p != "" {
		if err := v.ensureListed(parentOf(p)); err != nil {
			return "", err
		}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	id, ok := v.folders[p]
	if !ok {
		return "", notExist("stat", p)
	}
	return id, nil
}

// FileSize is the ciphertext size Drive reports.
func (v *VaultFS) FileSize(p string) (int64, error) {
	f, err := v.file(p)
	if err != nil {
		return 0, err
	}
	return f.Size, nil
}

// ReadDir lists a ciphertext directory.
func (v *VaultFS) ReadDir(dir string) ([]vault.DirEntry, error) {
	if _, err := v.folder(dir); err != nil {
		return nil, err
	}
	if err := v.ensureListed(dir); err != nil {
		return nil, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	var out []vault.DirEntry
	for p, f := range v.files {
		if p != dir && parentOf(p) == dir {
			out = append(out, vault.DirEntry{Name: path.Base(p), Size: f.Size})
		}
	}
	for p := range v.folders {
		if p != "" && p != dir && parentOf(p) == dir {
			out = append(out, vault.DirEntry{Name: path.Base(p), IsDir: true})
		}
	}
	return out, nil
}

// ReadFile reads a whole (small) file: gocryptfs.conf, diriv, name sidecars.
func (v *VaultFS) ReadFile(p string) ([]byte, error) {
	f, err := v.file(p)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, f.Size)
	n, err := v.ReadAt(p, 0, buf)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf[:n], nil
}

// ReadAt serves a range from the disk cache or fetches the window around it.
func (v *VaultFS) ReadAt(p string, off int64, buf []byte) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	f, err := v.file(p)
	if err != nil {
		return 0, err
	}
	return v.cache.ReadAt(meta(f), off, buf, func(ctx context.Context, off, n int64) ([]byte, error) {
		return v.c.ReadRange(ctx, f.ID, off, n)
	})
}

// meta is the cache identity of a Drive file version.
func meta(f File) cache.Meta {
	return cache.Meta{Key: f.ID, Version: f.ModifiedTime, Size: f.Size}
}

// --- Writes -------------------------------------------------------------------

// Exists reports whether a file or folder is at p.
func (v *VaultFS) Exists(p string) bool {
	if err := v.ensureListed(parentOf(p)); err != nil {
		return false
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	_, isFile := v.files[p]
	_, isDir := v.folders[p]
	return isFile || isDir
}

// Mkdir creates a folder.
func (v *VaultFS) Mkdir(p string) error {
	parent, err := v.folder(parentOf(p))
	if err != nil {
		return err
	}
	if v.Exists(p) {
		return &fs.PathError{Op: "mkdir", Path: p, Err: fs.ErrExist}
	}
	f, err := v.c.CreateFolder(context.Background(), parent, path.Base(p))
	if err != nil {
		return err
	}
	v.mu.Lock()
	v.folders[p] = f.ID
	v.listed[p] = true // it is new, so empty
	v.mu.Unlock()
	return nil
}

// WriteFile creates or replaces a small file.
func (v *VaultFS) WriteFile(p string, data []byte) error {
	parent, err := v.folder(parentOf(p))
	if err != nil {
		return err
	}
	var f *File
	if old, err := v.file(p); err == nil {
		f, err = v.c.Update(context.Background(), old.ID, "", data)
		if err != nil {
			return err
		}
		v.dropCache(old.ID)
	} else {
		f, err = v.c.Upload(context.Background(), parent, path.Base(p), "", data)
		if err != nil {
			return err
		}
	}
	v.mu.Lock()
	v.files[p] = *f
	v.mu.Unlock()
	return nil
}

// Remove deletes a file or folder (Drive deletes a folder's contents too).
func (v *VaultFS) Remove(p string) error {
	if p == "" {
		return &fs.PathError{Op: "remove", Path: p, Err: fs.ErrInvalid}
	}
	if err := v.ensureListed(parentOf(p)); err != nil {
		return err
	}
	v.mu.Lock()
	f, isFile := v.files[p]
	dirID, isDir := v.folders[p]
	v.mu.Unlock()
	var id string
	switch {
	case isFile:
		id = f.ID
	case isDir:
		id = dirID
	default:
		return notExist("remove", p)
	}
	if err := v.c.Delete(context.Background(), id); err != nil {
		return err
	}
	if isFile {
		v.dropCache(f.ID)
	}
	v.forget(p)
	return nil
}

// RemoveAll is Remove: Drive removes a folder with its contents.
func (v *VaultFS) RemoveAll(p string) error {
	err := v.Remove(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// forget drops p and everything under it from the index.
func (v *VaultFS) forget(p string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	under := func(k string) bool { return k == p || strings.HasPrefix(k, p+"/") }
	for k := range v.files {
		if under(k) {
			delete(v.files, k)
		}
	}
	for k := range v.folders {
		if under(k) {
			delete(v.folders, k)
			delete(v.listed, k)
		}
	}
}

// Rename moves and/or renames a file or folder, replacing a file already at
// newPath: Drive allows two entries with one name, which gocryptfs cannot.
func (v *VaultFS) Rename(oldPath, newPath string) error {
	if err := v.ensureListed(parentOf(oldPath)); err != nil {
		return err
	}
	oldParent, err := v.folder(parentOf(oldPath))
	if err != nil {
		return err
	}
	newParent, err := v.folder(parentOf(newPath))
	if err != nil {
		return err
	}
	v.mu.Lock()
	f, isFile := v.files[oldPath]
	dirID, isDir := v.folders[oldPath]
	v.mu.Unlock()
	if !isFile && !isDir {
		return notExist("rename", oldPath)
	}
	if v.Exists(newPath) {
		v.mu.Lock()
		_, targetIsDir := v.folders[newPath]
		v.mu.Unlock()
		if targetIsDir || isDir {
			return &fs.PathError{Op: "rename", Path: newPath, Err: fs.ErrExist}
		}
		if err := v.Remove(newPath); err != nil {
			return err
		}
	}
	id := dirID
	if isFile {
		id = f.ID
	}
	moved, err := v.c.Move(context.Background(), id, oldParent, newParent, path.Base(newPath))
	if err != nil {
		return err
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	if isFile {
		delete(v.files, oldPath)
		// Keep the old ModifiedTime: it is only the cache's version tag, and
		// the content did not change.
		f.Name = moved.Name
		v.files[newPath] = f
		return nil
	}
	rebase := func(k string) (string, bool) {
		if k == oldPath {
			return newPath, true
		}
		if strings.HasPrefix(k, oldPath+"/") {
			return newPath + k[len(oldPath):], true
		}
		return "", false
	}
	for k, e := range v.files {
		if nk, ok := rebase(k); ok {
			delete(v.files, k)
			v.files[nk] = e
		}
	}
	for k, id := range v.folders {
		if nk, ok := rebase(k); ok {
			delete(v.folders, k)
			v.folders[nk] = id
			if v.listed[k] {
				delete(v.listed, k)
				v.listed[nk] = true
			}
		}
	}
	return nil
}

// CreateTemp starts a streaming upload into dir under a temporary name. The
// vault engine writes ciphertext to it and then renames it into place.
func (v *VaultFS) CreateTemp(dir, pattern string) (io.WriteCloser, string, error) {
	parent, err := v.folder(dir)
	if err != nil {
		return nil, "", err
	}
	rnd := make([]byte, 8)
	_, _ = rand.Read(rnd)
	name := strings.Replace(pattern, "*", hex.EncodeToString(rnd), 1)
	if !strings.Contains(pattern, "*") {
		name = pattern + hex.EncodeToString(rnd)
	}
	up, err := v.c.NewUpload(context.Background(), parent, name, "", "")
	if err != nil {
		return nil, "", err
	}
	p := path.Join(dir, name)
	return &tempUpload{v: v, up: up, path: p}, p, nil
}

type tempUpload struct {
	v      *VaultFS
	up     *Upload
	path   string
	closed bool
}

func (t *tempUpload) Write(p []byte) (int, error) { return t.up.Write(p) }

// Close finishes the upload and adds the file to the index.
func (t *tempUpload) Close() error {
	if t.closed {
		return nil
	}
	t.closed = true
	f, err := t.up.Finish()
	if err != nil {
		t.up.Abort()
		return err
	}
	t.v.mu.Lock()
	t.v.files[t.path] = *f
	t.v.mu.Unlock()
	return nil
}

func (v *VaultFS) dropCache(fileID string) { v.cache.Drop(fileID) }

// Refresh forgets the listing of dir so the next access fetches it again,
// picking up changes made on other devices.
func (v *VaultFS) Refresh(dir string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.listed, dir)
	for k := range v.files {
		if k != dir && parentOf(k) == dir {
			delete(v.files, k)
		}
	}
	for k := range v.folders {
		if k != "" && k != dir && parentOf(k) == dir && !v.listed[k] {
			delete(v.folders, k)
		}
	}
}

// --- Offline caching (vault.Cacher) --------------------------------------------

// Cache reads the whole file through into the disk cache and pins it, so it
// opens without the network and is not evicted.
func (v *VaultFS) Cache(ctx context.Context, cipherPath string, opts xfer.Options) error {
	if v.cache.Ranges == nil {
		return errors.New("drive: no file cache configured")
	}
	f, err := v.file(cipherPath)
	if err != nil {
		return err
	}
	return v.cache.Pin(meta(f), func() error {
		r := xfer.NewReader(ctx, vault.RawOpener(v, cipherPath), opts)
		defer r.Close()
		_, err := io.Copy(io.Discard, r)
		return err
	})
}

// IsCached reports whether the whole file is held on disk.
func (v *VaultFS) IsCached(cipherPath string) bool {
	f, err := v.file(cipherPath)
	return err == nil && v.cache.Complete(meta(f))
}

// Uncache drops the file from the disk cache.
func (v *VaultFS) Uncache(cipherPath string) error {
	f, err := v.file(cipherPath)
	if err != nil {
		return err
	}
	v.dropCache(f.ID)
	return nil
}

// CachedFractions reports which parts of the file the disk cache holds, as
// [start, end) fractions of its length, for a "buffered" strip on a video
// scrubber. Ciphertext and plaintext offsets differ only by gocryptfs' fixed
// per-block overhead, so the fractions carry over.
func (v *VaultFS) CachedFractions(cipherPath string) [][2]float64 {
	f, err := v.file(cipherPath)
	if err != nil {
		return nil
	}
	return v.cache.Fractions(meta(f))
}

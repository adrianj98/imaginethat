package vault

import (
	"io"
	"os"
	"path/filepath"
	"sort"
)

// DirEntry is a single raw ciphertext directory entry as reported by a VFS.
// Names are still encrypted; sizes are ciphertext sizes.
type DirEntry struct {
	Name  string
	IsDir bool
	Size  int64
}

// VFS is the read side of a vault's underlying storage. Every path is a
// vault-relative *ciphertext* path, e.g. "gocryptfs.diriv" or
// "AbCdEf/gocryptfs.diriv".
//
// This mirrors the vaultVFS interface in gocryptfs-lib/main.go so a
// network-backed implementation (Google Drive, S3) can be dropped in later
// without touching the crypto.
type VFS interface {
	ReadAt(path string, offset int64, buf []byte) (int, error)
	FileSize(path string) (int64, error)
	ReadDir(path string) ([]DirEntry, error)
	ReadFile(path string) ([]byte, error)
}

// ReaderAtCloser is a persistent handle to one ciphertext file.
type ReaderAtCloser interface {
	io.ReaderAt
	io.Closer
}

// FileOpener is an optional VFS capability. Backends that implement it let
// *File hold one handle open for the life of a read session instead of
// reopening the ciphertext file on every block.
type FileOpener interface {
	OpenFile(path string) (ReaderAtCloser, error)
}

// WriteVFS is the write side of a vault's underlying storage. Only backends
// that can mutate storage implement it; read-only backends satisfy VFS alone
// and writes fail with ErrReadOnlyBackend.
type WriteVFS interface {
	VFS
	Mkdir(path string) error
	Remove(path string) error
	RemoveAll(path string) error
	Rename(oldPath, newPath string) error
	WriteFile(path string, data []byte) error
	// CreateTemp makes a temp file in dir (a vault-relative ciphertext
	// directory) and returns it plus its vault-relative path, so the caller
	// can stream ciphertext in and then Rename it into place.
	CreateTemp(dir, pattern string) (io.WriteCloser, string, error)
	Exists(path string) bool
}

// LocalFS is a VFS over a plain directory holding gocryptfs ciphertext.
type LocalFS struct {
	root string
}

// NewLocalFS returns a VFS rooted at the given ciphertext directory.
func NewLocalFS(root string) *LocalFS { return &LocalFS{root: root} }

var (
	_ VFS        = (*LocalFS)(nil)
	_ WriteVFS   = (*LocalFS)(nil)
	_ FileOpener = (*LocalFS)(nil)
)

// abs resolves a vault-relative ciphertext path against the vault root.
// Callers have already validated the plaintext path; the ciphertext components
// are base64 and cannot contain separators.
func (v *LocalFS) abs(path string) string {
	if path == "" || path == "." {
		return v.root
	}
	return filepath.Join(v.root, path)
}

func (v *LocalFS) ReadAt(path string, offset int64, buf []byte) (int, error) {
	f, err := os.Open(v.abs(path))
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return f.ReadAt(buf, offset)
}

func (v *LocalFS) OpenFile(path string) (ReaderAtCloser, error) {
	return os.Open(v.abs(path))
}

func (v *LocalFS) FileSize(path string) (int64, error) {
	fi, err := os.Stat(v.abs(path))
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

func (v *LocalFS) ReadDir(path string) ([]DirEntry, error) {
	entries, err := os.ReadDir(v.abs(path))
	if err != nil {
		return nil, err
	}
	out := make([]DirEntry, 0, len(entries))
	for _, e := range entries {
		var size int64
		if info, err := e.Info(); err == nil {
			size = info.Size()
		}
		out = append(out, DirEntry{Name: e.Name(), IsDir: e.IsDir(), Size: size})
	}
	// os.ReadDir already sorts by name, but make the contract explicit: the
	// ciphertext order is meaningless anyway, callers re-sort by plaintext.
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (v *LocalFS) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(v.abs(path))
}

func (v *LocalFS) Mkdir(path string) error { return os.Mkdir(v.abs(path), 0o700) }

func (v *LocalFS) Remove(path string) error { return os.Remove(v.abs(path)) }

func (v *LocalFS) RemoveAll(path string) error { return os.RemoveAll(v.abs(path)) }

func (v *LocalFS) Rename(oldPath, newPath string) error {
	return os.Rename(v.abs(oldPath), v.abs(newPath))
}

func (v *LocalFS) WriteFile(path string, data []byte) error {
	return os.WriteFile(v.abs(path), data, 0o600)
}

func (v *LocalFS) CreateTemp(dir, pattern string) (io.WriteCloser, string, error) {
	f, err := os.CreateTemp(v.abs(dir), pattern)
	if err != nil {
		return nil, "", err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, "", err
	}
	rel, err := filepath.Rel(v.root, f.Name())
	if err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, "", err
	}
	return f, rel, nil
}

func (v *LocalFS) Exists(path string) bool {
	_, err := os.Lstat(v.abs(path))
	return err == nil
}

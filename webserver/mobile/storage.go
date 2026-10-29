package mobile

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"path"
	"strings"

	"github.com/adrianm/imagethat/webserver/vault"
)

// Storage is implemented by the host (Kotlin, over Android SAF) to give the
// vault access to ciphertext files that have no filesystem path. All paths
// are relative to the vault root, slash separated; "" is the root.
//
// Error contract: errors cross gomobile as plain messages, so a missing
// file or directory must produce an error whose message starts with
// "not_found" (ReadAt, FileSize, ListDir and the others), and an
// already-existing target of Mkdir/Rename/OpenWriter one starting with
// "exists". The Go side maps these to fs.ErrNotExist / fs.ErrExist. Other
// errors are passed through unchanged.
//
// OpenWriter must fail (with "exists") if the file already exists, and must
// leave nothing behind when it fails.
type Storage interface {
	// ReadAt returns up to n bytes at offset; a short or empty result at
	// end of file is not an error.
	ReadAt(path string, offset int64, n int) ([]byte, error)
	FileSize(path string) (int64, error)
	// ListDir returns a JSON array of {"name","isDir","size"}.
	ListDir(path string) (string, error)
	WriteFile(path string, data []byte) error
	OpenWriter(path string) (StorageWriter, error)
	Mkdir(path string) error
	Remove(path string) error
	RemoveAll(path string) error
	Rename(from, to string) error
	Exists(path string) bool
}

// StorageWriter streams bytes into a new file.
type StorageWriter interface {
	Write(p []byte) error
	Close() error
}

// InitStorage creates a new empty vault in s.
func InitStorage(s Storage, password string) error {
	return wrapErr(vault.Init(storageVFS{s}, password))
}

// OpenStorage unlocks the vault stored in s.
func OpenStorage(s Storage, password string) (*Vault, error) {
	sess, err := vault.Open(storageVFS{s}, password)
	if err != nil {
		return nil, wrapErr(err)
	}
	return &Vault{s: sess}, nil
}

// storageVFS adapts Storage to vault.WriteVFS. It does not implement
// vault.FileOpener: SAF handles are reopened per read.
type storageVFS struct{ s Storage }

// conv translates the Storage error contract (message prefixes) into the
// sentinel errors the vault checks with os.IsNotExist / errors.Is. A
// *fs.PathError is used because os.IsNotExist does not unwrap %w chains.
func conv(op, p string, err error) error {
	switch {
	case err == nil:
		return nil
	case strings.HasPrefix(err.Error(), "not_found"):
		return &fs.PathError{Op: op, Path: p, Err: fs.ErrNotExist}
	case strings.HasPrefix(err.Error(), "exists"):
		return &fs.PathError{Op: op, Path: p, Err: fs.ErrExist}
	}
	return err
}

var _ vault.WriteVFS = storageVFS{}

func (v storageVFS) ReadAt(p string, offset int64, buf []byte) (int, error) {
	b, err := v.s.ReadAt(p, offset, len(buf))
	if err != nil {
		return 0, conv("read", p, err)
	}
	if len(b) > len(buf) {
		b = b[:len(buf)]
	}
	n := copy(buf, b)
	if n < len(buf) {
		return n, io.EOF
	}
	return n, nil
}

func (v storageVFS) FileSize(p string) (int64, error) {
	n, err := v.s.FileSize(p)
	return n, conv("stat", p, err)
}

func (v storageVFS) ReadDir(p string) ([]vault.DirEntry, error) {
	js, err := v.s.ListDir(p)
	if err != nil {
		return nil, conv("readdir", p, err)
	}
	var raw []struct {
		Name  string `json:"name"`
		IsDir bool   `json:"isDir"`
		Size  int64  `json:"size"`
	}
	if err := json.Unmarshal([]byte(js), &raw); err != nil {
		return nil, err
	}
	out := make([]vault.DirEntry, len(raw))
	for i, r := range raw {
		out[i] = vault.DirEntry{Name: r.Name, IsDir: r.IsDir, Size: r.Size}
	}
	return out, nil
}

func (v storageVFS) ReadFile(p string) ([]byte, error) {
	size, err := v.s.FileSize(p)
	if err != nil {
		return nil, conv("stat", p, err)
	}
	out := make([]byte, 0, size)
	for int64(len(out)) < size {
		b, err := v.s.ReadAt(p, int64(len(out)), int(min(size-int64(len(out)), 1<<20)))
		if err != nil {
			return nil, conv("read", p, err)
		}
		if len(b) == 0 {
			break
		}
		out = append(out, b...)
	}
	return out, nil
}

func (v storageVFS) Mkdir(p string) error               { return v.s.Mkdir(p) }
func (v storageVFS) Remove(p string) error              { return v.s.Remove(p) }
func (v storageVFS) RemoveAll(p string) error           { return v.s.RemoveAll(p) }
func (v storageVFS) Rename(from, to string) error       { return v.s.Rename(from, to) }
func (v storageVFS) WriteFile(p string, d []byte) error { return v.s.WriteFile(p, d) }
func (v storageVFS) Exists(p string) bool               { return v.s.Exists(p) }

type writerAdapter struct{ w StorageWriter }

func (a writerAdapter) Write(p []byte) (int, error) {
	if err := a.w.Write(p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (a writerAdapter) Close() error { return a.w.Close() }

// CreateTemp makes dir/<pattern> with the last "*" in pattern (or the end,
// if none) replaced by random hex; an empty pattern gives ".tmp-<random>".
func (v storageVFS) CreateTemp(dir, pattern string) (io.WriteCloser, string, error) {
	if pattern == "" {
		pattern = ".tmp-*"
	}
	prefix, suffix := pattern, ""
	if i := strings.LastIndex(pattern, "*"); i >= 0 {
		prefix, suffix = pattern[:i], pattern[i+1:]
	}
	for try := 0; try < 10; try++ {
		var r [8]byte
		if _, err := rand.Read(r[:]); err != nil {
			return nil, "", err
		}
		name := prefix + hex.EncodeToString(r[:]) + suffix
		p := name
		if dir != "" && dir != "." {
			p = path.Join(dir, name)
		}
		if v.s.Exists(p) {
			continue
		}
		w, err := v.s.OpenWriter(p)
		if err != nil {
			return nil, "", conv("create", p, err)
		}
		return writerAdapter{w}, p, nil
	}
	return nil, "", errors.New("mobile: could not create unique temp file")
}

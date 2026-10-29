package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/adrianm/imagethat/core/xfer"
)

// Files keeps whole copies of remote files on disk, one file per key, for
// files the user asked to have available locally. It stores only what it is
// handed, and callers hand it ciphertext: a vault's encrypted files, exactly
// as they sit on the server. Nothing in here is readable without the vault
// password.
//
// There is no eviction: every entry is one the user chose. Callers remove
// entries when the file changes, and ClearFiles empties the directory.
type Files struct {
	dir string
}

const filesTempPrefix = ".fetch-"

// staleTemp is how long a temp file can go unwritten before it counts as left
// over from a crash. A fetch in progress writes to its temp file constantly,
// so this never catches one that another session is still running.
const staleTemp = time.Hour

// NewFiles uses dir, creating it if needed, and removes temp files left by
// fetches a crash interrupted.
func NewFiles(dir string) (*Files, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if !strings.HasPrefix(e.Name(), filesTempPrefix) {
				continue
			}
			if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > staleTemp {
				_ = os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
	return &Files{dir: dir}, nil
}

// path hashes the key so keys can hold anything (URLs, ciphertext paths)
// without leaking it into a file name.
func (f *Files) path(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(f.dir, hex.EncodeToString(sum[:]))
}

// Open returns the cached copy, or an fs.ErrNotExist error.
func (f *Files) Open(key string) (*os.File, error) {
	return os.Open(f.path(key))
}

// Size returns the cached copy's size and whether there is one.
func (f *Files) Size(key string) (int64, bool) {
	info, err := os.Stat(f.path(key))
	if err != nil {
		return 0, false
	}
	return info.Size(), true
}

// Has reports whether key is cached.
func (f *Files) Has(key string) bool {
	_, ok := f.Size(key)
	return ok
}

// Remove drops key. A missing entry is not an error.
func (f *Files) Remove(key string) error {
	if err := os.Remove(f.path(key)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Fetch copies the source into the cache under key. Reads retry and resume
// (package xfer); the copy lands in a temp file, is synced, and is renamed
// into place only when complete, so Has never sees half a file.
func (f *Files) Fetch(ctx context.Context, key string, open xfer.Opener, opts xfer.Options) error {
	r := xfer.NewReader(ctx, open, opts)
	defer r.Close()
	size, err := r.Size()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(f.dir, filesTempPrefix+"*")
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	n, err := io.Copy(tmp, r)
	if err != nil {
		return err
	}
	if n != size {
		return fmt.Errorf("cache: fetched %d of %d bytes: %w", n, size, xfer.ErrTruncated)
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), f.path(key)); err != nil {
		return err
	}
	committed = true
	return nil
}

// ClearFiles deletes every cached file in dir.
func ClearFiles(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

// FilesSize is the total size of the cached files in dir.
func FilesSize(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

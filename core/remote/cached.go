package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"strings"

	"github.com/adrianm/imagethat/core/cache"
	"github.com/adrianm/imagethat/core/internal/gocryptfs/contentenc"
	"github.com/adrianm/imagethat/core/vault"
	"github.com/adrianm/imagethat/core/xfer"
)

// PermanentErrors are the remote errors a copy or cache fetch must not retry.
var PermanentErrors = []error{ErrWrongPassword, ErrLocked, ErrNotFound, ErrCertMismatch}

// CachedFS is a RawFS that can keep whole ciphertext files on disk, for files
// the user marked to keep locally. A cached file is read from disk; the rest
// come from the server as usual. Only ciphertext is stored, byte for byte as
// the server has it.
//
// Each time a cached file is opened its header and size are checked against
// the server in one small request. gocryptfs gives every write of a file a new
// random 16-byte ID in its header, so a file changed on the server never
// matches its stale copy, which is then dropped. When the server cannot be
// reached the local copy is used as it is: that is the point of caching it.
type CachedFS struct {
	*RawFS
	files  *cache.Files
	prefix string // keys entries by server and vault, so one directory serves all
}

var (
	_ vault.VFS        = (*CachedFS)(nil)
	_ vault.FileOpener = (*CachedFS)(nil)
	_ vault.Cacher     = (*CachedFS)(nil)
)

// OpenVaultCached is OpenVault with a file cache in cacheDir.
func (c *Client) OpenVaultCached(ctx context.Context, vaultID, password, cacheDir string) (*vault.Session, error) {
	files, err := cache.NewFiles(cacheDir)
	if err != nil {
		return nil, fmt.Errorf("remote: cache dir: %w", err)
	}
	if err := c.Unlock(ctx, vaultID, password); err != nil {
		return nil, err
	}
	raw := &RawFS{c: c, vaultID: vaultID}
	return vault.Open(&CachedFS{RawFS: raw, files: files, prefix: c.base.String() + "\x00" + vaultID + "\x00"}, password)
}

func (f *CachedFS) key(path string) string { return f.prefix + path }

// FileSize validates a cached copy (see CachedFS) before trusting it.
func (f *CachedFS) FileSize(path string) (int64, error) {
	key := f.key(path)
	localSize, ok := f.files.Size(key)
	if !ok {
		return f.RawFS.FileSize(path)
	}
	head, size, err := f.RawFS.headAndSize(path)
	switch {
	case err == nil:
	case errors.Is(err, fs.ErrNotExist):
		_ = f.files.Remove(key) // deleted on the server
		return 0, err
	case errors.Is(err, ErrCertMismatch), errors.Is(err, ErrWrongPassword), errors.Is(err, ErrLocked):
		return 0, err
	default:
		// Offline, timed out, or the server is failing: the local copy is
		// what we have, and the reason it was cached.
		return localSize, nil
	}
	if size != localSize || !f.headerMatches(key, head) {
		_ = f.files.Remove(key)
	}
	return size, nil
}

func (f *CachedFS) headerMatches(key string, head []byte) bool {
	local, err := f.files.Open(key)
	if err != nil {
		return false
	}
	defer local.Close()
	buf := make([]byte, len(head))
	n, _ := local.ReadAt(buf, 0)
	return n == len(head) && bytes.Equal(buf, head)
}

// ReadAt serves a cached file from disk and anything else from the server.
func (f *CachedFS) ReadAt(path string, off int64, buf []byte) (int, error) {
	if local, err := f.files.Open(f.key(path)); err == nil {
		defer local.Close()
		return local.ReadAt(buf, off)
	}
	return f.RawFS.ReadAt(path, off, buf)
}

// OpenFile hands vault.File the cached copy as a persistent handle; an
// uncached file reports an error so reads go through ReadAt.
func (f *CachedFS) OpenFile(path string) (vault.ReaderAtCloser, error) {
	return f.files.Open(f.key(path))
}

// Cache fetches one ciphertext file into the cache, retrying and resuming on
// network trouble.
func (f *CachedFS) Cache(ctx context.Context, path string, opts xfer.Options) error {
	// Already cached: FileSize checks the copy against the server and drops
	// it if stale, so only a changed file is fetched again.
	if f.IsCached(path) {
		if _, err := f.FileSize(path); err == nil && f.IsCached(path) {
			return nil
		}
	}
	opts.Permanent = append(append([]error(nil), opts.Permanent...), PermanentErrors...)
	return f.files.Fetch(ctx, f.key(path), vault.RawOpener(f.RawFS, path), opts)
}

// IsCached reports whether path has a local copy. It does not ask the server
// whether the copy is current; FileSize does that when the file is opened.
func (f *CachedFS) IsCached(path string) bool { return f.files.Has(f.key(path)) }

// Uncache drops path's local copy.
func (f *CachedFS) Uncache(path string) error { return f.files.Remove(f.key(path)) }

// headAndSize fetches a file's gocryptfs header and its total size in one
// ranged request. An empty file has no header.
func (r *RawFS) headAndSize(path string) ([]byte, int64, error) {
	h := http.Header{"Range": {fmt.Sprintf("bytes=0-%d", contentenc.HeaderLen-1)}}
	resp, err := r.get(http.MethodGet, "file", path, h)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusPartialContent:
		size, err := totalFromContentRange(resp.Header.Get("Content-Range"))
		if err != nil {
			return nil, 0, err
		}
		head, err := io.ReadAll(io.LimitReader(resp.Body, contentenc.HeaderLen))
		return head, size, err
	case http.StatusOK:
		// A file shorter than the range comes back whole.
		head, err := io.ReadAll(io.LimitReader(resp.Body, contentenc.HeaderLen))
		if err != nil {
			return nil, 0, err
		}
		if resp.ContentLength >= 0 {
			return head, resp.ContentLength, nil
		}
		return head, int64(len(head)), nil
	case http.StatusRequestedRangeNotSatisfiable:
		return nil, 0, nil // empty file
	default:
		return nil, 0, status(resp, "stat", path)
	}
}

// totalFromContentRange parses the size out of "bytes 0-17/12345".
func totalFromContentRange(v string) (int64, error) {
	i := strings.LastIndexByte(v, '/')
	if i < 0 {
		return 0, fmt.Errorf("remote: bad Content-Range %q", v)
	}
	n, err := strconv.ParseInt(v[i+1:], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("remote: bad Content-Range %q", v)
	}
	return n, nil
}

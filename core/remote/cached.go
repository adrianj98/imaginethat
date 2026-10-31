package remote

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/adrianm/imagethat/core/cache"
	"github.com/adrianm/imagethat/core/cachefs"
	"github.com/adrianm/imagethat/core/internal/gocryptfs/contentenc"
	"github.com/adrianm/imagethat/core/vault"
)

// PermanentErrors are the remote errors a copy or cache fetch must not retry.
var PermanentErrors = []error{ErrWrongPassword, ErrLocked, ErrNotFound, ErrCertMismatch}

// OpenVaultCached is OpenVault behind the cache layer (cachefs): reads go
// through ranges, the read-through cache every network transport shares
// (cache.SharedRanges), so a file read once, or pinned with Cache File, comes
// off disk afterwards, also offline. Only ciphertext is stored, byte for byte
// as the server has it.
func (c *Client) OpenVaultCached(ctx context.Context, vaultID, password string, ranges *cache.Ranges) (*vault.Session, error) {
	// As in OpenVault: a client-decrypt server (after Login) never gets the
	// vault password; a server-decrypt one must unlock before serving raw files.
	if _, ok := c.loggedIn(); !ok {
		if err := c.Unlock(ctx, vaultID, password); err != nil {
			return nil, err
		}
	}
	raw := &RawFS{c: c, vaultID: vaultID}
	cfs := cachefs.New(raw, ranges, c.base.String()+"\x00"+vaultID+"\x00", PermanentErrors...)
	s, err := vault.Open(cfs, password)
	if errors.Is(err, vault.ErrWrongPassword) {
		return nil, ErrWrongPassword
	}
	if err != nil && errors.Is(err, vault.ErrNoConfig) {
		return nil, ErrNotFound
	}
	return s, err
}

// Stat is a file's cache version: its gocryptfs header and its size, in one
// small request. gocryptfs gives every write of a file a new random 16-byte
// ID in its header, so a file changed on the server never matches what is
// held for it.
func (r *RawFS) Stat(path string) (string, int64, error) {
	head, size, err := r.headAndSize(path)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(head) + "/" + strconv.FormatInt(size, 10), size, nil
}

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

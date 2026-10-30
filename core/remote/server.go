package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/adrianm/imagethat/core/vault"
)

// The server-decrypt API: the server holds the vault unlocked for this
// client's session and serves decrypted listings, files and thumbnails, and
// takes uploads and edits. These calls are what the apps use for a
// server-decrypt server; a client-decrypt server is opened with OpenVault.

var (
	// ErrExists means the target of an upload, mkdir or move already exists.
	ErrExists = errors.New("remote: already exists")
	// ErrForbidden means the server refused the write (read-only vault, or
	// its OS user cannot write there).
	ErrForbidden = errors.New("remote: the server refused the change")
)

// VaultInfo is one vault the server offers.
type VaultInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Unlocked bool   `json:"unlocked"`
}

// apiError reads the server's {"error": "..."} body into an error matching
// the package's sentinels.
func apiError(resp *http.Response, op string) error {
	defer resp.Body.Close()
	var body struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body)
	msg := body.Error
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	var sentinel error
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		sentinel = ErrLocked
	case http.StatusNotFound:
		sentinel = ErrNotFound
	case http.StatusConflict:
		sentinel = ErrExists
	case http.StatusForbidden:
		sentinel = ErrForbidden
	default:
		return fmt.Errorf("remote: %s: HTTP %d: %s", op, resp.StatusCode, msg)
	}
	return fmt.Errorf("%w (%s: %s)", sentinel, op, msg)
}

func (c *Client) vaultURL(id, op string, q url.Values) string {
	return c.endpoint("/api/vaults/"+url.PathEscape(id)+"/"+op, q)
}

// call sends one vault request through do (which logs in or unlocks again
// after the server's idle timeout) and checks the status.
func (c *Client) call(ctx context.Context, vaultID, method, u string, body []byte, header http.Header, op string) (*http.Response, error) {
	resp, err := c.do(vaultID, func() (*http.Request, error) {
		var r io.Reader
		if body != nil {
			r = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, u, r)
		if err != nil {
			return nil, err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		for k, v := range header {
			req.Header[k] = v
		}
		return req, nil
	})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, apiError(resp, op)
	}
	return resp, nil
}

func (c *Client) postJSON(ctx context.Context, vaultID, op string, body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := c.call(ctx, vaultID, http.MethodPost, c.vaultURL(vaultID, op, nil), data, nil, op)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// Vaults lists the server's vaults. A client-decrypt server answers only
// after Login.
func (c *Client) Vaults(ctx context.Context) ([]VaultInfo, error) {
	resp, err := c.call(ctx, "", http.MethodGet, c.endpoint("/api/vaults", nil), nil, nil, "vaults")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var body struct {
		Vaults []VaultInfo `json:"vaults"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("remote: vaults: %w", err)
	}
	return body.Vaults, nil
}

// Lock asks the server to forget the vault's keys for this session.
func (c *Client) Lock(ctx context.Context, vaultID string) error {
	c.mu.Lock()
	delete(c.passwords, vaultID)
	c.mu.Unlock()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.vaultURL(vaultID, "lock", nil), nil)
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return wrapTransport(err)
	}
	resp.Body.Close()
	return nil
}

// Logout ends the session on the server, locking everything it held, and
// forgets every password this client kept.
func (c *Client) Logout(ctx context.Context) error {
	c.mu.Lock()
	c.passwords = make(map[string]string)
	c.accessPassword = ""
	c.mu.Unlock()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint("/api/logout", nil), nil)
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return wrapTransport(err)
	}
	resp.Body.Close()
	return nil
}

// List returns a decrypted directory listing.
func (c *Client) List(ctx context.Context, vaultID, dir string) ([]vault.Entry, error) {
	resp, err := c.call(ctx, vaultID, http.MethodGet, c.vaultURL(vaultID, "list", url.Values{"path": {dir}}), nil, nil, "list")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var body struct {
		Entries []vault.Entry `json:"entries"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("remote: list %s: %w", dir, err)
	}
	return body.Entries, nil
}

// FileSize is a file's decrypted size.
func (c *Client) FileSize(ctx context.Context, vaultID, p string) (int64, error) {
	resp, err := c.call(ctx, vaultID, http.MethodHead, c.vaultURL(vaultID, "file", url.Values{"path": {p}}), nil, nil, "stat")
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	if resp.ContentLength < 0 {
		return 0, fmt.Errorf("remote: no size for %s", p)
	}
	return resp.ContentLength, nil
}

// Open streams a file from offset off; length < 0 means to the end. The
// returned size is the whole file's.
func (c *Client) Open(ctx context.Context, vaultID, p string, off, length int64) (io.ReadCloser, int64, error) {
	h := http.Header{}
	if off > 0 || length >= 0 {
		end := ""
		if length >= 0 {
			if length == 0 {
				return io.NopCloser(bytes.NewReader(nil)), 0, nil
			}
			end = strconv.FormatInt(off+length-1, 10)
		}
		h.Set("Range", fmt.Sprintf("bytes=%d-%s", off, end))
	}
	resp, err := c.do(vaultID, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.vaultURL(vaultID, "file", url.Values{"path": {p}}), nil)
		if err == nil {
			req.Header = h.Clone()
		}
		return req, err
	})
	if err != nil {
		return nil, 0, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		if off > 0 {
			if _, err := io.CopyN(io.Discard, resp.Body, off); err != nil {
				resp.Body.Close()
				return io.NopCloser(bytes.NewReader(nil)), resp.ContentLength, nil
			}
		}
		return resp.Body, resp.ContentLength, nil
	case http.StatusPartialContent:
		total, err := totalFromContentRange(resp.Header.Get("Content-Range"))
		if err != nil {
			resp.Body.Close()
			return nil, 0, err
		}
		return resp.Body, total, nil
	case http.StatusRequestedRangeNotSatisfiable:
		resp.Body.Close()
		total, _ := totalFromContentRange(resp.Header.Get("Content-Range"))
		return io.NopCloser(bytes.NewReader(nil)), total, nil
	default:
		return nil, 0, apiError(resp, "read")
	}
}

// ReadRange reads up to n decrypted bytes at off; fewer at the end of the file.
func (c *Client) ReadRange(ctx context.Context, vaultID, p string, off int64, n int) ([]byte, error) {
	rc, _, err := c.Open(ctx, vaultID, p, off, int64(n))
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, int64(n)))
}

// Thumbnail returns the server's JPEG preview, or nil when it has none
// (formats it cannot decode; the app renders those itself).
func (c *Client) Thumbnail(ctx context.Context, vaultID, p string, size int) ([]byte, error) {
	resp, err := c.call(ctx, vaultID, http.MethodGet,
		c.vaultURL(vaultID, "thumb", url.Values{"path": {p}, "size": {strconv.Itoa(size)}}), nil, nil, "thumbnail")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	return io.ReadAll(io.LimitReader(resp.Body, 32<<20))
}

// Upload streams r into the vault as dir/name; the server encrypts it as it
// arrives. size may be -1 when unknown.
func (c *Client) Upload(ctx context.Context, vaultID, dir, name string, r io.Reader, size int64) error {
	// The body can only be sent once, so make sure the session is alive (and
	// unlocked again after an idle timeout) with a cheap request first.
	if _, err := c.List(ctx, vaultID, dir); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.vaultURL(vaultID, "upload", url.Values{"path": {dir}, "name": {name}}), r)
	if err != nil {
		return err
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/octet-stream")
	// No overall timeout for a long upload; ctx still cancels it.
	hc := *c.hc
	hc.Timeout = 0
	resp, err := hc.Do(req)
	if err != nil {
		return wrapTransport(err)
	}
	if resp.StatusCode >= 300 {
		return apiError(resp, "upload")
	}
	resp.Body.Close()
	return nil
}

// Mkdir creates a directory.
func (c *Client) Mkdir(ctx context.Context, vaultID, p string) error {
	return c.postJSON(ctx, vaultID, "mkdir", map[string]string{"path": p})
}

// Move renames or moves a file or directory.
func (c *Client) Move(ctx context.Context, vaultID, from, to string) error {
	return c.postJSON(ctx, vaultID, "move", map[string]string{"from": from, "to": to})
}

// Delete removes a file or directory.
func (c *Client) Delete(ctx context.Context, vaultID, p string) error {
	resp, err := c.call(ctx, vaultID, http.MethodDelete, c.vaultURL(vaultID, "entry", url.Values{"path": {p}}), nil, nil, "delete")
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

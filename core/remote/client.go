// Package remote opens a vault served by an imagethat-web server in
// ciphertext mode: the server sends the vault's gocryptfs files as they are
// on disk (its /raw endpoints) and decryption happens here, with the
// password the client already holds. What crosses the network and anything a
// client caches is ciphertext, exactly as for a vault in Google Drive.
//
// The server's decrypted endpoints are unaffected; writes still go through
// them, since a vault opened here is read-only.
package remote

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/adrianm/imagethat/core/vault"
)

// Errors a caller can tell apart with errors.Is.
var (
	// ErrWrongPassword means the server refused the password.
	ErrWrongPassword = errors.New("remote: wrong password")
	// ErrLocked means the server no longer holds the vault unlocked and
	// re-unlocking did not help.
	ErrLocked = errors.New("remote: vault is locked on the server")
	// ErrNotFound means the server has no such vault or path.
	ErrNotFound = errors.New("remote: not found")
	// ErrCertMismatch means the server's certificate is not the pinned one.
	ErrCertMismatch = errors.New("remote: server certificate does not match the pinned fingerprint")
)

// Options configures a Client.
type Options struct {
	// PinnedSHA256 is the lowercase hex SHA-256 of the server's leaf
	// certificate, for self-signed servers. Empty uses the system trust store.
	PinnedSHA256 string
	// Timeout bounds one request. Default 60s.
	Timeout time.Duration
	// Transport overrides the HTTP transport, for tests.
	Transport http.RoundTripper
}

// Client talks to one imagethat-web server. Its session cookie and the
// passwords it re-unlocks with live in memory only.
type Client struct {
	base *url.URL
	hc   *http.Client

	mu        sync.Mutex
	passwords map[string]string // vault ID -> password, for re-unlock after the server's idle timeout
}

// New returns a client for the server at baseURL (e.g. https://nas:8347).
func New(baseURL string, opts Options) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("remote: invalid server URL %q", baseURL)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 60 * time.Second
	}
	rt := opts.Transport
	if rt == nil {
		t := http.DefaultTransport.(*http.Transport).Clone()
		if pin := strings.ToLower(strings.ReplaceAll(opts.PinnedSHA256, ":", "")); pin != "" {
			t.TLSClientConfig = pinnedTLS(pin)
		}
		rt = t
	}
	return &Client{
		base:      u,
		hc:        &http.Client{Jar: jar, Timeout: opts.Timeout, Transport: rt},
		passwords: make(map[string]string),
	}, nil
}

// pinnedTLS trusts exactly the certificate whose SHA-256 is pin, the way the
// Android app pins a self-signed server.
func pinnedTLS(pin string) *tls.Config {
	return &tls.Config{
		InsecureSkipVerify: true, // replaced by the fingerprint check below
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return ErrCertMismatch
			}
			sum := sha256.Sum256(raw[0])
			if hex.EncodeToString(sum[:]) != pin {
				return ErrCertMismatch
			}
			return nil
		},
	}
}

func (c *Client) endpoint(path string, query url.Values) string {
	u := *c.base
	u.Path = c.base.Path + path
	u.RawQuery = query.Encode()
	return u.String()
}

// Unlock proves the password to the server, which then holds the vault
// unlocked for this client's session.
func (c *Client) Unlock(ctx context.Context, vaultID, password string) error {
	body, _ := json.Marshal(map[string]string{"password": password})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.endpoint("/api/vaults/"+url.PathEscape(vaultID)+"/unlock", nil), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return wrapTransport(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	switch resp.StatusCode {
	case http.StatusOK:
		c.mu.Lock()
		c.passwords[vaultID] = password
		c.mu.Unlock()
		return nil
	case http.StatusUnauthorized:
		return ErrWrongPassword
	case http.StatusNotFound:
		return ErrNotFound
	default:
		return fmt.Errorf("remote: unlock: HTTP %d", resp.StatusCode)
	}
}

// OpenVault unlocks the vault on the server and opens it here over the
// server's ciphertext endpoints. The session decrypts locally; it is
// read-only.
func (c *Client) OpenVault(ctx context.Context, vaultID, password string) (*vault.Session, error) {
	if err := c.Unlock(ctx, vaultID, password); err != nil {
		return nil, err
	}
	return vault.Open(&RawFS{c: c, vaultID: vaultID}, password)
}

// do sends a request for vaultID, re-unlocking once if the server has expired
// the session (its idle timeout) and the password is known.
func (c *Client) do(vaultID string, build func() (*http.Request, error)) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		req, err := build()
		if err != nil {
			return nil, err
		}
		resp, err := c.hc.Do(req)
		if err != nil {
			return nil, wrapTransport(err)
		}
		if resp.StatusCode != http.StatusUnauthorized || attempt > 0 {
			return resp, nil
		}
		resp.Body.Close()
		c.mu.Lock()
		pw, ok := c.passwords[vaultID]
		c.mu.Unlock()
		if !ok {
			return nil, ErrLocked
		}
		if err := c.Unlock(req.Context(), vaultID, pw); err != nil {
			if errors.Is(err, ErrWrongPassword) {
				return nil, err
			}
			return nil, fmt.Errorf("%w: re-unlock failed: %v", ErrLocked, err)
		}
	}
}

func wrapTransport(err error) error {
	if errors.Is(err, ErrCertMismatch) {
		return ErrCertMismatch
	}
	return fmt.Errorf("remote: %w", err)
}

// RawFS is a vault.VFS over a server's ciphertext endpoints. Every path is a
// vault-relative ciphertext path.
type RawFS struct {
	c       *Client
	vaultID string
}

var _ vault.VFS = (*RawFS)(nil)

func (r *RawFS) url(op, path string) string {
	return r.c.endpoint("/api/vaults/"+url.PathEscape(r.vaultID)+"/raw/"+op, url.Values{"path": {path}})
}

func (r *RawFS) get(method, op, path string, header http.Header) (*http.Response, error) {
	return r.c.do(r.vaultID, func() (*http.Request, error) {
		req, err := http.NewRequest(method, r.url(op, path), nil)
		if err != nil {
			return nil, err
		}
		for k, v := range header {
			req.Header[k] = v
		}
		return req, nil
	})
}

// status turns an unexpected response into an error that vault understands:
// a missing path must be fs.ErrNotExist so it reports ErrNotFound.
func status(resp *http.Response, op, path string) error {
	switch resp.StatusCode {
	case http.StatusNotFound:
		return &fs.PathError{Op: op, Path: path, Err: fs.ErrNotExist}
	case http.StatusUnauthorized:
		return ErrLocked
	default:
		return fmt.Errorf("remote: %s %s: HTTP %d", op, path, resp.StatusCode)
	}
}

// ReadAt fetches one byte range. A short result at the end of the file comes
// with io.EOF, as io.ReaderAt requires.
func (r *RawFS) ReadAt(path string, off int64, buf []byte) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	h := http.Header{"Range": {fmt.Sprintf("bytes=%d-%d", off, off+int64(len(buf))-1)}}
	resp, err := r.get(http.MethodGet, "file", path, h)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusPartialContent:
	case http.StatusRequestedRangeNotSatisfiable:
		return 0, io.EOF
	case http.StatusOK:
		// The server sent the whole file; skip to the offset.
		if _, err := io.CopyN(io.Discard, resp.Body, off); err != nil {
			return 0, io.EOF
		}
	default:
		return 0, status(resp, "read", path)
	}
	n, err := io.ReadFull(resp.Body, buf)
	if errors.Is(err, io.ErrUnexpectedEOF) || (errors.Is(err, io.EOF) && n == 0) {
		return n, io.EOF
	}
	return n, err
}

// FileSize asks for the ciphertext size with a HEAD request.
func (r *RawFS) FileSize(path string) (int64, error) {
	resp, err := r.get(http.MethodHead, "file", path, nil)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, status(resp, "stat", path)
	}
	if resp.ContentLength < 0 {
		return 0, fmt.Errorf("remote: no size for %s", path)
	}
	return resp.ContentLength, nil
}

// ReadDir lists a ciphertext directory.
func (r *RawFS) ReadDir(path string) ([]vault.DirEntry, error) {
	resp, err := r.get(http.MethodGet, "list", path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, status(resp, "readdir", path)
	}
	var body struct {
		Entries []struct {
			Name  string `json:"name"`
			IsDir bool   `json:"isDir"`
			Size  int64  `json:"size"`
		} `json:"entries"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("remote: list %s: %w", path, err)
	}
	out := make([]vault.DirEntry, len(body.Entries))
	for i, e := range body.Entries {
		out[i] = vault.DirEntry{Name: e.Name, IsDir: e.IsDir, Size: e.Size}
	}
	return out, nil
}

// ReadFile fetches a whole (small) ciphertext file: gocryptfs.conf,
// gocryptfs.diriv, long-name sidecars.
func (r *RawFS) ReadFile(path string) ([]byte, error) {
	resp, err := r.get(http.MethodGet, "file", path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, status(resp, "read", path)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}

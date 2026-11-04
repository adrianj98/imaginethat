package remote

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
)

// The write side of RawFS: plain file operations on ciphertext paths, over a
// client-decrypt server's raw write endpoints. vault.Session does all of the
// gocryptfs work on top (encrypting content and names, gocryptfs.diriv,
// long-name sidecars, temp file then rename), so the server only ever
// receives ciphertext, exactly as Google Drive does.

func (r *RawFS) endpoint(op string, query url.Values) string {
	return r.c.endpoint("/api/vaults/"+url.PathEscape(r.vaultID)+"/raw/"+op, query)
}

// send makes one small mutating request; do() logs in again and resends it
// if the server's session has expired.
func (r *RawFS) send(method, op, path string, query url.Values, body []byte, contentType string) error {
	resp, err := r.c.do(func() (*http.Request, error) {
		req, err := http.NewRequest(method, r.endpoint(op, query), bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		return req, nil
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return status(resp, op, path)
	}
	return nil
}

// Exists reports whether a ciphertext file or directory exists. Any error,
// including a network one, reads as "no".
func (r *RawFS) Exists(path string) bool {
	resp, err := r.get(http.MethodHead, "entry", path, nil)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func (r *RawFS) Mkdir(path string) error {
	return r.send(http.MethodPost, "mkdir", path, url.Values{"path": {path}}, nil, "")
}

func (r *RawFS) Remove(path string) error {
	return r.send(http.MethodDelete, "entry", path, url.Values{"path": {path}}, nil, "")
}

func (r *RawFS) RemoveAll(path string) error {
	err := r.send(http.MethodDelete, "entry", path, url.Values{"path": {path}, "recursive": {"1"}}, nil, "")
	// os.RemoveAll succeeds on a missing path; so does this.
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func (r *RawFS) Rename(oldPath, newPath string) error {
	body, _ := json.Marshal(map[string]string{"from": oldPath, "to": newPath})
	return r.send(http.MethodPost, "rename", oldPath, nil, body, "application/json")
}

// WriteFile creates or replaces a small file (gocryptfs.diriv, a long-name
// sidecar). The server writes it to a temp file and renames it into place.
func (r *RawFS) WriteFile(path string, data []byte) error {
	return r.send(http.MethodPut, "file", path, url.Values{"path": {path}}, data, "application/octet-stream")
}

// CreateTemp starts a streamed upload into dir under a random name. What is
// written goes out as one chunked PUT; Close finishes it and reports how the
// server took it. The vault engine then renames the file into place.
//
// A streamed body cannot be sent twice, so unlike the other requests this one
// is not retried after a login. Checking that dir exists first goes through
// do(), which logs in again if the server's session has expired.
func (r *RawFS) CreateTemp(dir, pattern string) (io.WriteCloser, string, error) {
	if !r.Exists(dir) {
		// Exists hides why; ask again for the real error.
		if _, err := r.ReadDir(dir); err != nil {
			return nil, "", err
		}
		return nil, "", &fs.PathError{Op: "create", Path: dir, Err: fs.ErrNotExist}
	}
	rnd := make([]byte, 8)
	_, _ = rand.Read(rnd)
	name := strings.Replace(pattern, "*", hex.EncodeToString(rnd), 1)
	if !strings.Contains(pattern, "*") {
		name = pattern + hex.EncodeToString(rnd)
	}
	p := path.Join(dir, name)

	pr, pw := io.Pipe()
	req, err := http.NewRequest(http.MethodPut, r.endpoint("file", url.Values{"path": {p}}), pr)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	up := &rawUpload{pw: pw, done: make(chan error, 1)}
	go func() {
		resp, err := r.c.stream.Do(req)
		if err != nil {
			err = wrapTransport(err)
		} else {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				err = status(resp, "write", p)
			}
		}
		// A server that gave up early must not leave Write blocked.
		pr.CloseWithError(fmt.Errorf("remote: upload ended: %w", orClosed(err)))
		up.done <- err
	}()
	return up, p, nil
}

func orClosed(err error) error {
	if err == nil {
		return io.ErrClosedPipe
	}
	return err
}

// rawUpload is the writer CreateTemp returns.
type rawUpload struct {
	pw   *io.PipeWriter
	done chan error

	once sync.Once
	err  error
}

func (u *rawUpload) Write(p []byte) (int, error) { return u.pw.Write(p) }

// Close ends the body and waits for the server's answer. Calling it again
// returns the same result.
func (u *rawUpload) Close() error {
	u.once.Do(func() {
		u.pw.Close()
		u.err = <-u.done
	})
	return u.err
}

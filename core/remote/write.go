package remote

import (
	"bytes"
	"context"
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
	"time"

	"github.com/adrianm/imagethat/core/vault"
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
	return r.CreateTempContext(context.Background(), dir, pattern)
}

var _ vault.ContextTempCreator = (*RawFS)(nil)

// CreateTempContext is CreateTemp for a copy that can be cancelled:
// cancelling ctx ends the upload, including one blocked on a link that
// stands still.
//
// The upload has no overall time limit, since a large file over a slow link
// takes as long as it takes. What it must not do is wait forever on a
// connection that has gone silent, so it gives up once the link has taken
// nothing for the client's Timeout while there were bytes to send, or the
// server has not answered for that long after the last byte. Time the caller
// spends between writes does not count: its source may be retrying a read
// for minutes.
func (r *RawFS) CreateTempContext(ctx context.Context, dir, pattern string) (io.WriteCloser, string, error) {
	if !r.Exists(dir) {
		// Exists says no for any failure, a network one included, so ask
		// again in a way that says why. A listing that works means the
		// folder is there and the first answer was a blip: carry on. Calling
		// that "does not exist" would stop a copy for good over a moment's
		// outage.
		if _, err := r.ReadDir(dir); err != nil {
			return nil, "", err
		}
	}
	rnd := make([]byte, 8)
	_, _ = rand.Read(rnd)
	name := strings.Replace(pattern, "*", hex.EncodeToString(rnd), 1)
	if !strings.Contains(pattern, "*") {
		name = pattern + hex.EncodeToString(rnd)
	}
	p := path.Join(dir, name)

	ctx, cancel := context.WithCancelCause(ctx)
	idle := r.c.idle
	stalled := fmt.Errorf("remote: upload stalled, nothing moved for %v: %w", idle, context.DeadlineExceeded)
	up := &rawUpload{done: make(chan error, 1), cancel: cancel}
	up.watch.t = time.AfterFunc(idle, func() { cancel(stalled) })
	up.watch.t.Stop()
	up.watch.d = idle

	pr, pw := io.Pipe()
	up.pw = pw
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, r.endpoint("file", url.Values{"path": {p}}),
		progressReader{pr, &up.watch})
	if err != nil {
		cancel(nil)
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	go func() {
		resp, err := r.c.stream.Do(req)
		if err != nil {
			// The request was ended on purpose: say why (a stall, or the
			// caller's cancel) in place of the transport's "canceled".
			if cause := context.Cause(ctx); cause != nil {
				err = cause
			}
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

// stallWatch fires when an upload that has something to send, or is waiting
// for its answer, sees no movement for d.
type stallWatch struct {
	mu    sync.Mutex
	t     *time.Timer
	d     time.Duration
	armed bool
}

func (w *stallWatch) arm() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.armed = true
	w.t.Reset(w.d)
}

func (w *stallWatch) disarm() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.armed = false
	w.t.Stop()
}

// moved is called when the link takes bytes.
func (w *stallWatch) moved() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.armed {
		w.t.Reset(w.d)
	}
}

// progressReader is the request body: each read is the transport taking more
// bytes for the wire.
type progressReader struct {
	io.Reader
	watch *stallWatch
}

func (p progressReader) Read(b []byte) (int, error) {
	n, err := p.Reader.Read(b)
	p.watch.moved()
	return n, err
}

// rawUpload is the writer CreateTemp returns.
type rawUpload struct {
	pw     *io.PipeWriter
	done   chan error
	cancel context.CancelCauseFunc
	watch  stallWatch

	once sync.Once
	err  error
}

func (u *rawUpload) Write(p []byte) (int, error) {
	u.watch.arm()
	defer u.watch.disarm()
	return u.pw.Write(p)
}

// Close ends the body and waits for the server's answer. Calling it again
// returns the same result.
func (u *rawUpload) Close() error {
	u.once.Do(func() {
		u.watch.arm()
		u.pw.Close()
		u.err = <-u.done
		u.watch.disarm()
		u.cancel(nil)
	})
	return u.err
}

package drive

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
	"strings"
	"time"
)

// uploadChunk is how much an Upload buffers before sending it. Drive wants
// resumable chunks in multiples of 256 KiB.
var uploadChunk = 8 << 20

// Upload streams a new file (or new content for an existing one) to Drive
// without knowing its size up front. Small files go up as one multipart
// request on Finish; anything past one chunk uses a resumable session, so a
// failed chunk is resent rather than the whole file.
type Upload struct {
	c        *Client
	ctx      context.Context
	meta     map[string]any
	mimeType string
	fileID   string // set when replacing an existing file's content

	session string // resumable session URL, once started
	buf     []byte
	sent    int64
	err     error
	done    bool
}

// NewUpload starts an upload. For a new file pass parentID and name; to
// replace a file's content pass its ID as updateID instead.
func (c *Client) NewUpload(ctx context.Context, parentID, name, mimeType, updateID string) (*Upload, error) {
	if _, err := c.token(ctx, false); err != nil {
		return nil, err
	}
	meta := map[string]any{}
	if updateID == "" {
		meta["name"] = name
		meta["parents"] = []string{parentID}
	}
	return &Upload{c: c, ctx: ctx, meta: meta, mimeType: mimeOr(mimeType), fileID: updateID}, nil
}

// Write buffers p and sends every full chunk.
func (u *Upload) Write(p []byte) (int, error) {
	if u.err != nil {
		return 0, u.err
	}
	if u.done {
		return 0, errors.New("drive: write after finish")
	}
	u.buf = append(u.buf, p...)
	for len(u.buf) >= uploadChunk {
		if err := u.sendChunk(u.buf[:uploadChunk], false); err != nil {
			u.err = err
			return 0, err
		}
		u.buf = append(u.buf[:0], u.buf[uploadChunk:]...)
	}
	return len(p), nil
}

// Finish sends what is left and returns the stored file.
func (u *Upload) Finish() (*File, error) {
	if u.err != nil {
		return nil, u.err
	}
	if u.done {
		return nil, errors.New("drive: upload already finished")
	}
	u.done = true
	if u.session == "" {
		if u.fileID != "" {
			return u.c.multipart(u.ctx, http.MethodPatch, u.fileID, u.meta, u.mimeType, u.buf)
		}
		return u.c.multipart(u.ctx, http.MethodPost, "", u.meta, u.mimeType, u.buf)
	}
	f, err := u.finalChunk()
	u.buf = nil
	return f, err
}

// Abort cancels a resumable session so Drive drops what it received.
func (u *Upload) Abort() {
	u.done = true
	u.buf = nil
	if u.session == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if resp, err := u.c.do(ctx, func() (*http.Request, error) { return http.NewRequest(http.MethodDelete, u.session, nil) }); err == nil {
		resp.Body.Close()
	}
}

func (u *Upload) start() error {
	meta, err := json.Marshal(u.meta)
	if err != nil {
		return err
	}
	method, path := http.MethodPost, "/upload/drive/v3/files"
	if u.fileID != "" {
		method, path = http.MethodPatch, path+"/"+url.PathEscape(u.fileID)
	}
	resp, err := u.c.do(u.ctx, func() (*http.Request, error) {
		req, err := http.NewRequest(method, u.c.api(path, url.Values{"uploadType": {"resumable"}, "fields": {fileFields}}), bytes.NewReader(meta))
		if err == nil {
			req.Header.Set("Content-Type", "application/json; charset=UTF-8")
			req.Header.Set("X-Upload-Content-Type", u.mimeType)
		}
		return req, err
	})
	if err != nil {
		return err
	}
	if err := check(resp); err != nil {
		return err
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if loc == "" {
		return errors.New("drive: no resumable upload location")
	}
	u.session = loc
	return nil
}

const chunkAttempts = 5

// sendChunk sends data as the next part of the file. A failure is retried
// after asking Drive how much it has, so nothing is sent twice.
func (u *Upload) sendChunk(data []byte, final bool) error {
	if u.session == "" {
		if err := u.start(); err != nil {
			return err
		}
	}
	_, err := u.put(data, final)
	return err
}

func (u *Upload) finalChunk() (*File, error) {
	if u.session == "" {
		if err := u.start(); err != nil {
			return nil, err
		}
	}
	return u.put(u.buf, true)
}

func (u *Upload) put(data []byte, final bool) (*File, error) {
	delay := 500 * time.Millisecond
	for attempt := 1; ; attempt++ {
		total := "*"
		if final {
			total = strconv.FormatInt(u.sent+int64(len(data)), 10)
		}
		rng := "bytes */" + total
		if len(data) > 0 {
			rng = fmt.Sprintf("bytes %d-%d/%s", u.sent, u.sent+int64(len(data))-1, total)
		}
		resp, err := u.c.do(u.ctx, func() (*http.Request, error) {
			req, err := http.NewRequest(http.MethodPut, u.session, bytes.NewReader(data))
			if err == nil {
				req.Header.Set("Content-Range", rng)
			}
			return req, err
		})
		if err == nil {
			switch {
			case resp.StatusCode == http.StatusPermanentRedirect && !final:
				resp.Body.Close()
				u.sent += int64(len(data))
				return nil, nil
			case resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated:
				defer resp.Body.Close()
				var f File
				if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&f); err != nil {
					return nil, fmt.Errorf("drive: upload response: %w", err)
				}
				f.fix()
				u.sent += int64(len(data))
				return &f, nil
			case resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode != http.StatusPermanentRedirect:
				return nil, check(resp)
			}
			resp.Body.Close()
			err = fmt.Errorf("drive: upload chunk: HTTP %d", resp.StatusCode)
		}
		if attempt >= chunkAttempts || u.ctx.Err() != nil {
			return nil, err
		}
		select {
		case <-u.ctx.Done():
			return nil, u.ctx.Err()
		case <-time.After(delay):
		}
		delay *= 2
		// Drive may have kept part of the chunk; carry on from what it has.
		have, qerr := u.received()
		if qerr == nil && have > u.sent && have <= u.sent+int64(len(data)) {
			data = data[have-u.sent:]
			u.sent = have
		}
	}
}

// received asks the resumable session how many bytes it holds.
func (u *Upload) received() (int64, error) {
	resp, err := u.c.do(u.ctx, func() (*http.Request, error) {
		req, err := http.NewRequest(http.MethodPut, u.session, nil)
		if err == nil {
			req.Header.Set("Content-Range", "bytes */*")
		}
		return req, err
	})
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusPermanentRedirect {
		return 0, fmt.Errorf("drive: upload status: HTTP %d", resp.StatusCode)
	}
	r := resp.Header.Get("Range") // "bytes=0-1234"
	if r == "" {
		return 0, nil
	}
	i := strings.LastIndex(r, "-")
	if i < 0 {
		return 0, fmt.Errorf("drive: bad Range %q", r)
	}
	n, err := strconv.ParseInt(r[i+1:], 10, 64)
	if err != nil {
		return 0, err
	}
	return n + 1, nil
}

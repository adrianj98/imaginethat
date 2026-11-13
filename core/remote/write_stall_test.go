package remote

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/adrianm/imagethat/core/vault"
	"github.com/adrianm/imagethat/core/xfer"
)

// uploadServer answers every request with 200 except the upload, which goes
// to put.
func uploadServer(t *testing.T, put http.HandlerFunc) *RawFS {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			put(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)
	c, err := New(ts.URL, Options{Timeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return &RawFS{c: c, vaultID: "v"}
}

// within fails the test if f takes longer than d.
func within(t *testing.T, d time.Duration, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); f() }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not return within %v", what, d)
	}
}

// A server that takes the bytes and then never answers must not hold an
// upload forever: the client gives up after its timeout with an error a copy
// will retry.
func TestUploadWithNoAnswerTimesOut(t *testing.T) {
	release := make(chan struct{})
	fs := uploadServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-release
	})
	t.Cleanup(func() { close(release) }) // before the server closes, which waits for the handler
	w, _, err := fs.CreateTemp("", "tmp-*")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(make([]byte, 1024)); err != nil {
		t.Fatal(err)
	}
	within(t, 2*time.Second, "Close", func() { err = w.Close() })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Close returned %v, want a deadline error", err)
	}
	if !xfer.Retryable(err) {
		t.Errorf("%v is not retryable, so a copy would give up on a stall", err)
	}
}

// A server that stops reading must not hold a write forever either.
func TestUploadNobodyReadsTimesOut(t *testing.T) {
	release := make(chan struct{})
	fs := uploadServer(t, func(w http.ResponseWriter, r *http.Request) { <-release })
	t.Cleanup(func() { close(release) }) // before the server closes, which waits for the handler
	w, _, err := fs.CreateTemp("", "tmp-*")
	if err != nil {
		t.Fatal(err)
	}
	within(t, 3*time.Second, "Write", func() {
		// Far more than the socket buffers hold.
		chunk := make([]byte, 1<<20)
		for range 64 {
			if _, err = w.Write(chunk); err != nil {
				return
			}
		}
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Write returned %v, want a deadline error", err)
	}
	_ = w.Close()
}

// The timeout is about the link standing still. A source that pauses for
// longer than the timeout between writes (it may be retrying a read for
// minutes) is not a stalled upload.
func TestUploadSurvivesAPausingSource(t *testing.T) {
	var got int64
	fs := uploadServer(t, func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	})
	w, _, err := fs.CreateTemp("", "tmp-*")
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := w.Write(make([]byte, 1024)); err != nil {
			t.Fatalf("Write after a pause: %v", err)
		}
		time.Sleep(250 * time.Millisecond) // the client's timeout is 100ms
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got != 3*1024 {
		t.Errorf("server got %d bytes, want %d", got, 3*1024)
	}
}

// Cancelling must stop an upload that is blocked on a link that stands
// still, at once and not at the timeout.
func TestCancelStopsABlockedUpload(t *testing.T) {
	release := make(chan struct{})
	fs := uploadServer(t, func(w http.ResponseWriter, r *http.Request) { <-release })
	t.Cleanup(func() { close(release) }) // before the server closes, which waits for the handler
	fs.c.idle = time.Hour                // only the cancel can end this one

	var _ vault.ContextTempCreator = fs
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, _, err := fs.CreateTempContext(ctx, "", "tmp-*")
	if err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(100*time.Millisecond, cancel)
	within(t, 2*time.Second, "Write", func() {
		chunk := make([]byte, 1<<20)
		for range 64 {
			if _, err = w.Write(chunk); err != nil {
				return
			}
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Write returned %v, want context.Canceled", err)
	}
	_ = w.Close()
}

// The check that the folder exists can itself hit a network blip. That must
// not be reported as a missing folder, which a copy would never retry.
func TestUploadSurvivesABlipInTheFolderCheck(t *testing.T) {
	var heads, got int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			heads++
			// Drop the connection without an answer.
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
		case http.MethodGet:
			_, _ = io.WriteString(w, `{"entries":[]}`)
		case http.MethodPut:
			got, _ = io.Copy(io.Discard, r.Body)
		}
	}))
	t.Cleanup(ts.Close)
	c, err := New(ts.URL, Options{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	fs := &RawFS{c: c, vaultID: "v"}

	w, _, err := fs.CreateTemp("dir", "tmp-*")
	if err != nil {
		t.Fatalf("CreateTemp after a dropped folder check: %v", err)
	}
	if _, err := w.Write(make([]byte, 1024)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if heads == 0 || got != 1024 {
		t.Errorf("folder checks %d, uploaded %d bytes; want at least one check and 1024 bytes", heads, got)
	}
}

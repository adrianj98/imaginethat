package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/adrianm/imagethat/webserver/vault"
)

// videoThumbnailer extracts a single frame from a vault video by piping the
// decrypted stream through an external ffmpeg process.
//
// Plaintext never touches disk: the video is served to ffmpeg over a one-shot
// loopback HTTP listener, ffmpeg decodes in memory and writes the JPEG frame
// to stdout. ffmpeg's HTTP input does Range requests, so even a multi-GB file
// costs roughly its moov atom plus one GOP.
//
// One caveat, accepted deliberately: the loopback URL (with its random token)
// appears in ffmpeg's argv, so another local user could read it from the
// process list during the seconds the capture runs and fetch that one video.
// This server already trusts the machine it runs on far more than that — the
// vault keys sit in its heap.
type videoThumbnailer struct {
	ffmpeg string
	// ffmpeg decodes are the most expensive thing this server does; a page of
	// fresh video tiles must queue rather than fork a decoder per tile.
	sem chan struct{}
}

func newVideoThumbnailer(ffmpegPath string) *videoThumbnailer {
	return &videoThumbnailer{ffmpeg: ffmpegPath, sem: make(chan struct{}, 2)}
}

// captureTimeout bounds one ffmpeg run. Generous: the source may live on a
// slow NAS volume and HEVC decode on weak hardware is not quick.
const captureTimeout = 60 * time.Second

// capture returns a JPEG frame from around the one-second mark of the video.
func (v *videoThumbnailer) capture(ctx context.Context, f *vault.File, name string, size int) ([]byte, error) {
	select {
	case v.sem <- struct{}{}:
		defer func() { <-v.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	ctx, cancel := context.WithTimeout(ctx, captureTimeout)
	defer cancel()

	url, shutdown, err := serveFileOnce(f, name)
	if err != nil {
		return nil, err
	}
	defer shutdown()

	// -ss before -i seeks via Range requests instead of decoding from the
	// start. Try one second in (past the black or blank first frame most
	// encodes open with), then fall back to the very first frame for clips
	// shorter than that.
	for _, seek := range []string{"1", "0"} {
		jpeg, err := v.run(ctx, url, seek, size)
		if err != nil {
			return nil, err
		}
		if len(jpeg) > 0 {
			return jpeg, nil
		}
	}
	return nil, fmt.Errorf("ffmpeg produced no frame for %q", name)
}

func (v *videoThumbnailer) run(ctx context.Context, url, seek string, size int) ([]byte, error) {
	args := []string{
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-ss", seek,
		"-i", url,
		"-frames:v", "1",
		"-vf", fmt.Sprintf("scale=w=%d:h=%d:force_original_aspect_ratio=decrease", size, size),
		"-f", "mjpeg", "pipe:1",
	}
	cmd := exec.CommandContext(ctx, v.ffmpeg, args...)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("ffmpeg: %w", ctx.Err())
		}
		msg := strings.TrimSpace(errBuf.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("ffmpeg: %s", msg)
	}
	return out.Bytes(), nil
}

// serveFileOnce exposes one decrypted file at a secret URL on a fresh
// loopback-only listener, and returns that URL plus a shutdown func.
func serveFileOnce(f *vault.File, name string) (string, func(), error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}

	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		ln.Close()
		return "", nil, err
	}
	secret := "/" + hex.EncodeToString(tokenBytes)

	// ServeContent seeks the shared *vault.File, so requests must not overlap;
	// ffmpeg opens sequential connections for its range reads, but nothing
	// forces it to, and a retried request can briefly coexist with its
	// predecessor.
	var mu sync.Mutex
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != secret {
				http.NotFound(w, r)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			w.Header().Set("Content-Type", vault.ContentType(name))
			noStore(w)
			http.ServeContent(w, r, name, time.Time{}, f)
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }()

	shutdown := func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}
	return fmt.Sprintf("http://%s%s", ln.Addr().String(), secret), shutdown, nil
}

package server

import (
	"net/http"
	"path"
	"strconv"
	"time"

	"github.com/adrianm/imagethat/core/thumb"
	"github.com/adrianm/imagethat/core/vault"
)

// noStore marks a response as never cacheable.
//
// The browser's HTTP cache is on disk, and this body is decrypted vault
// content. no-store is the only lever the server has to keep it out of there,
// so every response carrying plaintext sets it.
func noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, private")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
}

// handleFile serves a decrypted file with full byte-range support.
//
// vault.File is an io.ReadSeeker, so http.ServeContent does all the Range,
// 206 and If-Range work; blocks are decrypted on demand as it seeks.
func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	u, ok := s.requireVault(w, r)
	if !ok {
		return
	}
	p, ok := requestPath(w, r)
	if !ok {
		return
	}
	if p == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}

	f, err := u.sess.Open(p)
	if err != nil {
		s.writeVaultError(w, err, "open")
		return
	}
	defer f.Close()

	name := path.Base(p)
	w.Header().Set("Content-Type", vault.ContentType(name))
	// Let the browser render media inline but never execute it as part of the
	// site: a vault could hold an .html or .svg file.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; media-src 'self'; img-src 'self'")
	if r.URL.Query().Get("download") != "" {
		w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+urlEncode(name))
	}
	noStore(w)

	// A zero modtime keeps ServeContent from emitting Last-Modified, which
	// would otherwise let a cache revalidate content we said not to store.
	http.ServeContent(w, r, name, time.Time{}, f)
}

// handleThumb returns a downscaled JPEG preview.
//
// Results are cached per vault in an encrypted, in-memory LRU: a thumbnail is
// derived from vault content and is just as sensitive as the original, so it
// is sealed with a key that dies when the vault locks.
func (s *Server) handleThumb(w http.ResponseWriter, r *http.Request) {
	u, ok := s.requireVault(w, r)
	if !ok {
		return
	}
	p, ok := requestPath(w, r)
	if !ok {
		return
	}
	if p == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}

	size := 320
	if raw := r.URL.Query().Get("size"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 32 && n <= 2048 {
			size = n
		}
	}

	if !vault.IsBrowserDecodableImage(path.Base(p)) {
		if s.videoThumbs != nil && vault.ClassifyMedia(path.Base(p)) == vault.MediaVideo {
			s.serveVideoThumb(w, r, u, p, size)
			return
		}
		// HEIC and RAW need decoders the server does not have, and without
		// ffmpeg the same goes for video; the UI then draws a placeholder or
		// captures a frame itself.
		noStore(w)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Include the plaintext size in the key so an overwritten file does not
	// serve a stale preview.
	plainSize, err := u.sess.FileSize(p)
	if err != nil {
		s.writeVaultError(w, err, "thumbnail")
		return
	}
	key := p + "|" + strconv.Itoa(size) + "|" + strconv.FormatInt(plainSize, 10)

	if cached, hit := u.thumbs.Get(key); hit {
		s.writeThumb(w, cached)
		return
	}

	if plainSize > maxThumbSourceBytes {
		noStore(w)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	src, err := u.sess.ReadFile(p)
	if err != nil {
		s.writeVaultError(w, err, "thumbnail")
		return
	}

	jpegBytes, err := thumb.Make(src, size)
	// The decoded source is no longer needed; do not leave a decoded copy of
	// vault content lying in the heap any longer than necessary.
	for i := range src {
		src[i] = 0
	}
	if err != nil {
		noStore(w)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	u.thumbs.Put(key, jpegBytes)
	s.writeThumb(w, jpegBytes)
}

// serveVideoThumb answers a thumbnail request for a video by running ffmpeg
// over the decrypted stream. Failures degrade to 204 so the browser falls
// back to capturing a frame itself.
func (s *Server) serveVideoThumb(w http.ResponseWriter, r *http.Request, u *unlockedVault, p string, size int) {
	plainSize, err := u.sess.FileSize(p)
	if err != nil {
		s.writeVaultError(w, err, "video thumbnail")
		return
	}
	// The "video|" prefix keeps these keys from ever colliding with image
	// thumbnails of the same path and size.
	key := "video|" + p + "|" + strconv.Itoa(size) + "|" + strconv.FormatInt(plainSize, 10)

	if cached, hit := u.thumbs.Get(key); hit {
		s.writeThumb(w, cached)
		return
	}

	f, err := u.sess.Open(p)
	if err != nil {
		s.writeVaultError(w, err, "video thumbnail")
		return
	}
	defer f.Close()

	jpegBytes, err := s.videoThumbs.capture(r.Context(), f, path.Base(p), size)
	if err != nil {
		s.cfg.Logger.Printf("video thumbnail %q: %v", p, err)
		noStore(w)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	u.thumbs.Put(key, jpegBytes)
	s.writeThumb(w, jpegBytes)
}

// maxThumbSourceBytes caps how large a file the server will decode whole to
// make a preview.
const maxThumbSourceBytes = 100 << 20

func (s *Server) writeThumb(w http.ResponseWriter, jpegBytes []byte) {
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.Itoa(len(jpegBytes)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	noStore(w)
	_, _ = w.Write(jpegBytes)
}

// urlEncode percent-encodes a filename for Content-Disposition.
func urlEncode(s string) string {
	const hex = "0123456789ABCDEF"
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '.' || c == '_' || c == '~' {
			out = append(out, c)
			continue
		}
		out = append(out, '%', hex[c>>4], hex[c&0x0f])
	}
	return string(out)
}

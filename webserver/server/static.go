package server

import (
	"embed"
	_ "embed"
	"io/fs"
	"net/http"
	"strings"
)

// staticFiles holds the built SPA, which Vite writes into this directory
// (go:embed cannot reach outside its own package). Only .gitkeep is committed;
// everything else appears at build time.
//
// The "all:" prefix is what makes the pattern match when .gitkeep is the only
// file present, so `go build` works in a fresh clone with no Node toolchain.
//
//go:embed all:dist
var staticFiles embed.FS

// placeholderHTML is served when no SPA bundle has been built, so a
// freshly-cloned checkout explains itself instead of returning a bare 404.
//
//go:embed placeholder.html
var placeholderHTML []byte

// staticHandler serves the embedded SPA, falling back to index.html so
// client-side routes survive a page reload.
func (s *Server) staticHandler() http.Handler {
	sub, err := fs.Sub(staticFiles, "dist")
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "static assets unavailable", http.StatusInternalServerError)
		})
	}
	index, indexErr := fs.ReadFile(sub, "index.html")
	built := indexErr == nil

	files := http.FS(sub)
	fileServer := http.FileServer(files)

	serveIndex := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		if built {
			_, _ = w.Write(index)
		} else {
			_, _ = w.Write(placeholderHTML)
		}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /api is routed elsewhere; anything reaching here matched no
		// endpoint, and serving index.html would mask the 404.
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeError(w, http.StatusNotFound, "no such endpoint")
			return
		}

		// The service worker answers /decrypted/ in client-decrypt mode. A
		// request reaching the server means it is not running; fail plainly
		// rather than hand an <img> or <video> the SPA's HTML.
		if strings.HasPrefix(r.URL.Path, "/decrypted/") {
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, "the decrypting service worker is not running; reload the page", http.StatusNotFound)
			return
		}

		upath := strings.TrimPrefix(r.URL.Path, "/")
		if upath == "" || upath == "index.html" {
			serveIndex(w)
			return
		}

		if f, err := files.Open(upath); err == nil {
			f.Close()
			// Vite fingerprints asset filenames, so they are safe to cache
			// forever; index.html is not, because it names the current bundle.
			if strings.HasPrefix(upath, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			fileServer.ServeHTTP(w, r)
			return
		}

		// Unknown path: hand the SPA its entry point so client-side routing
		// survives a reload.
		serveIndex(w)
	})
}

package server

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/adrianm/imagethat/core/vault"
)

// The raw endpoints serve a vault's gocryptfs files as they are on disk:
// gocryptfs.conf, ciphertext directory listings and ciphertext file bytes.
// A client that holds the password opens the vault itself over them
// (core/remote) and decrypts locally, so it can cache what it fetches without
// ever writing plaintext. The decrypted endpoints stay as they are for
// browsers and for writes.
//
// They need the same unlocked session as everything else. Ciphertext alone is
// harmless, but with gocryptfs.conf it allows offline password guessing, so
// only a client that has already proved the password gets either.
//
// Paths here are ciphertext paths relative to the vault root, slash separated.

// rawEntry is one ciphertext directory entry.
type rawEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size"`
}

// rawPath resolves a ciphertext path inside the vault directory, refusing
// anything that would leave it, including through a symlink.
func (s *Server) rawPath(w http.ResponseWriter, r *http.Request) (string, bool) {
	vaultDir, ok := s.vaultPath(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "no such vault")
		return "", false
	}
	clean, err := vault.CleanPath(r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid path")
		return "", false
	}
	root, err := filepath.EvalSymlinks(vaultDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "vault unavailable")
		return "", false
	}
	full := filepath.Join(root, filepath.FromSlash(clean))
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			writeError(w, http.StatusNotFound, "not found")
		} else {
			writeError(w, http.StatusInternalServerError, "raw read failed")
		}
		return "", false
	}
	if resolved != root && !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
		writeError(w, http.StatusBadRequest, "invalid path")
		return "", false
	}
	return resolved, true
}

// handleRawList lists one ciphertext directory, names still encrypted.
func (s *Server) handleRawList(w http.ResponseWriter, r *http.Request) {
	if !s.requireRawAccess(w, r) {
		return
	}
	dir, ok := s.rawPath(w, r)
	if !ok {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		s.cfg.Logger.Printf("raw list: %v", err)
		if errors.Is(err, fs.ErrNotExist) {
			writeError(w, http.StatusNotFound, "not found")
		} else {
			writeError(w, http.StatusBadRequest, "not a directory")
		}
		return
	}
	out := make([]rawEntry, 0, len(entries))
	for _, e := range entries {
		var size int64
		if !e.IsDir() {
			if info, err := e.Info(); err == nil {
				size = info.Size()
			}
		}
		out = append(out, rawEntry{Name: e.Name(), IsDir: e.IsDir(), Size: size})
	}
	// The directory IV and long-name sidecars ride along, so a client can
	// decrypt the whole listing from this one response.
	resp := rawListResponse{Entries: out, LongNames: map[string]string{}}
	if iv, err := readSmall(filepath.Join(dir, dirIVName), 64); err == nil {
		resp.DirIV = iv
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, longNamePrefix) || strings.HasSuffix(name, longNameSuffix) {
			continue
		}
		if b, err := readSmall(filepath.Join(dir, name+longNameSuffix), maxLongNameFile); err == nil {
			resp.LongNames[name] = string(b)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

const (
	dirIVName      = "gocryptfs.diriv"
	longNamePrefix = "gocryptfs.longname."
	longNameSuffix = ".name"
	// A long-name sidecar holds one base64 encrypted name; gocryptfs names are
	// at most 255 bytes, so this is generous.
	maxLongNameFile = 4096
)

// rawListResponse is a ciphertext directory listing. DirIV is the
// directory's gocryptfs.diriv (base64 in JSON), absent for a plaintext-names
// vault. LongNames maps each gocryptfs.longname.* entry to its sidecar's
// content, the full encrypted name.
type rawListResponse struct {
	Entries   []rawEntry        `json:"entries"`
	DirIV     []byte            `json:"dirIV,omitempty"`
	LongNames map[string]string `json:"longNames"`
}

// readSmall reads a regular file of at most limit bytes, refusing symlinks
// so a planted link cannot pull in a file from outside the vault.
func readSmall(p string, limit int64) ([]byte, error) {
	info, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, fs.ErrInvalid
	}
	return os.ReadFile(p)
}

// handleRawFile serves ciphertext bytes, with Range and HEAD support. The
// vault's gocryptfs.conf is the file at path "gocryptfs.conf".
func (s *Server) handleRawFile(w http.ResponseWriter, r *http.Request) {
	if !s.requireRawAccess(w, r) {
		return
	}
	p, ok := s.rawPath(w, r)
	if !ok {
		return
	}
	f, err := os.Open(p)
	if err != nil {
		s.cfg.Logger.Printf("raw open: %v", err)
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || info.IsDir() {
		writeError(w, http.StatusBadRequest, "not a file")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Ciphertext is safe to cache in principle, but these responses only go
	// to authenticated clients that keep their own cache; keep proxies and
	// browsers out of it.
	noStore(w)
	http.ServeContent(w, r, "", time.Time{}, f)
}

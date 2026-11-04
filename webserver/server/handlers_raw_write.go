package server

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/adrianm/imagethat/core/vault"
)

// The raw write endpoints let a client-decrypt client change a vault without
// the server ever seeing plaintext, a key or a password. The client's
// vault.Session does all of the gocryptfs work (encrypting content and names,
// gocryptfs.diriv, long-name sidecars, temp file then rename) and uses these
// as plain file operations on ciphertext paths, the way it uses Google Drive.
//
// They exist only in client-decrypt mode. A server-decrypt server keeps its
// own sessions (cached directory IVs, thumbnails) over the same files, which
// writes from outside would leave stale; it has the decrypted write endpoints
// instead.
//
// What they refuse, whoever asks: anything outside the vault (".." or a
// symlink leading out), the vault root itself, writing through a symlink, and
// gocryptfs.conf, without which every file in the vault is unreadable.

const (
	configName = "gocryptfs.conf"
	// Our own temp files, so an interrupted PUT never leaves a partial file
	// under the real name.
	rawTempPattern = ".imagethat-raw-*"
)

// rawTarget resolves a ciphertext path that a write will create, replace or
// remove. The parent must exist and be inside the vault; the final component
// need not exist, but if it does it must not be a symlink.
func (s *Server) rawTarget(w http.ResponseWriter, r *http.Request, raw string) (string, bool) {
	vaultDir, ok := s.vaultPath(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "no such vault")
		return "", false
	}
	clean, err := vault.CleanPath(raw)
	if err != nil || clean == "" {
		writeError(w, http.StatusBadRequest, "invalid path")
		return "", false
	}
	if clean == configName {
		writeError(w, http.StatusForbidden, "the vault config cannot be changed")
		return "", false
	}
	root, err := filepath.EvalSymlinks(vaultDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "vault unavailable")
		return "", false
	}
	parentRel, base := path.Split(clean)
	parent, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(parentRel)))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			writeError(w, http.StatusNotFound, "not found")
		} else {
			writeError(w, http.StatusInternalServerError, "raw write failed")
		}
		return "", false
	}
	if parent != root && !strings.HasPrefix(parent, root+string(filepath.Separator)) {
		writeError(w, http.StatusBadRequest, "invalid path")
		return "", false
	}
	full := filepath.Join(parent, base)
	if info, err := os.Lstat(full); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		writeError(w, http.StatusBadRequest, "invalid path")
		return "", false
	}
	return full, true
}

// handleRawEntry answers HEAD with 200 when the path exists, file or
// directory, and 404 when it does not.
func (s *Server) handleRawEntry(w http.ResponseWriter, r *http.Request) {
	if !s.requireRawAccess(w, r) {
		return
	}
	if _, ok := s.rawPath(w, r); !ok {
		return
	}
	noStore(w)
	w.WriteHeader(http.StatusOK)
}

// handleRawPut writes the request body to a ciphertext path, creating or
// replacing it. The body goes to a temp file in the same directory first and
// is renamed into place only once it has all arrived.
func (s *Server) handleRawPut(w http.ResponseWriter, r *http.Request) {
	if !s.checkOrigin(w, r) || !s.requireRawAccess(w, r) {
		return
	}
	target, ok := s.rawTarget(w, r, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	if info, err := os.Lstat(target); err == nil && info.IsDir() {
		writeError(w, http.StatusConflict, "a directory is in the way")
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), rawTempPattern)
	if err != nil {
		s.cfg.Logger.Printf("raw put: %v", err)
		writeError(w, http.StatusInternalServerError, "raw write failed")
		return
	}
	committed := false
	defer func() {
		if !committed {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	if _, err := io.Copy(tmp, r.Body); err != nil {
		s.cfg.Logger.Printf("raw put: %v", err)
		writeError(w, http.StatusBadRequest, "upload interrupted")
		return
	}
	if err := tmp.Close(); err != nil {
		s.cfg.Logger.Printf("raw put: %v", err)
		writeError(w, http.StatusInternalServerError, "raw write failed")
		return
	}
	// gocryptfs makes its bookkeeping files read-only; match it, as a local
	// write through vault.LocalFS would.
	if isBookkeeping(filepath.Base(target)) {
		_ = os.Chmod(tmp.Name(), 0o444)
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		s.cfg.Logger.Printf("raw put: %v", err)
		writeError(w, http.StatusInternalServerError, "raw write failed")
		return
	}
	committed = true
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func isBookkeeping(name string) bool {
	return name == dirIVName ||
		(strings.HasPrefix(name, longNamePrefix) && strings.HasSuffix(name, longNameSuffix))
}

// handleRawMkdir creates one ciphertext directory. The client writes its
// gocryptfs.diriv next.
func (s *Server) handleRawMkdir(w http.ResponseWriter, r *http.Request) {
	if !s.checkOrigin(w, r) || !s.requireRawAccess(w, r) {
		return
	}
	target, ok := s.rawTarget(w, r, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	if err := os.Mkdir(target, 0o700); err != nil {
		s.writeRawError(w, err, "mkdir")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type rawRenameRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// handleRawRename renames a ciphertext path, replacing a file already at the
// destination (vault.Session.Replace relies on that).
func (s *Server) handleRawRename(w http.ResponseWriter, r *http.Request) {
	if !s.checkOrigin(w, r) || !s.requireRawAccess(w, r) {
		return
	}
	var req rawRenameRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxJSONBody)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "malformed request body")
		return
	}
	from, ok := s.rawTarget(w, r, req.From)
	if !ok {
		return
	}
	to, ok := s.rawTarget(w, r, req.To)
	if !ok {
		return
	}
	if err := os.Rename(from, to); err != nil {
		s.writeRawError(w, err, "rename")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleRawDelete removes a ciphertext path; with recursive=1 a whole
// directory tree.
func (s *Server) handleRawDelete(w http.ResponseWriter, r *http.Request) {
	if !s.checkOrigin(w, r) || !s.requireRawAccess(w, r) {
		return
	}
	target, ok := s.rawTarget(w, r, r.URL.Query().Get("path"))
	if !ok {
		return
	}
	if _, err := os.Lstat(target); err != nil {
		s.writeRawError(w, err, "delete")
		return
	}
	var err error
	if r.URL.Query().Get("recursive") == "1" {
		err = os.RemoveAll(target)
	} else {
		err = os.Remove(target)
	}
	if err != nil {
		s.writeRawError(w, err, "delete")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) writeRawError(w http.ResponseWriter, err error, op string) {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, fs.ErrExist):
		writeError(w, http.StatusConflict, "already exists")
	case errors.Is(err, fs.ErrPermission):
		writeError(w, http.StatusForbidden, "permission denied")
	default:
		s.cfg.Logger.Printf("raw %s: %v", op, err)
		writeError(w, http.StatusInternalServerError, "raw "+op+" failed")
	}
}

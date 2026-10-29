package server

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/adrianm/imagethat/core/cache"
	"github.com/adrianm/imagethat/core/vault"
)

// maxJSONBody bounds request bodies for the small JSON endpoints.
const maxJSONBody = 64 << 10

// routes wires up the API. Everything under /api is JSON except the media
// endpoints; anything else falls through to the embedded SPA.
func (s *Server) routes() {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/vaults", s.handleListVaults)
	mux.HandleFunc("POST /api/vaults/{id}/unlock", s.handleUnlock)
	mux.HandleFunc("POST /api/vaults/{id}/lock", s.handleLock)
	mux.HandleFunc("GET /api/vaults/{id}/list", s.handleList)
	mux.HandleFunc("GET /api/vaults/{id}/file", s.handleFile)
	mux.HandleFunc("GET /api/vaults/{id}/thumb", s.handleThumb)
	mux.HandleFunc("POST /api/vaults/{id}/upload", s.handleUpload)
	mux.HandleFunc("POST /api/vaults/{id}/mkdir", s.handleMkdir)
	mux.HandleFunc("POST /api/vaults/{id}/move", s.handleMove)
	mux.HandleFunc("DELETE /api/vaults/{id}/entry", s.handleDelete)
	mux.HandleFunc("POST /api/logout", s.handleLogout)

	// Ciphertext access for clients that decrypt themselves (handlers_raw.go).
	mux.HandleFunc("GET /api/vaults/{id}/raw/list", s.handleRawList)
	mux.HandleFunc("GET /api/vaults/{id}/raw/file", s.handleRawFile)

	mux.Handle("/", s.staticHandler())

	s.mux = mux
}

// handleListVaults reports the discovered vaults and which are unlocked.
//
// It deliberately does not create a session. Minting one per cookie-less
// request would let anyone who can reach the port allocate unbounded
// server-side state, and with -idle-timeout=0 nothing would ever reap it.
// Sessions are created only by /unlock, which is rate limited.
func (s *Server) handleListVaults(w http.ResponseWriter, r *http.Request) {
	b, _ := s.existingSession(r)
	// b is nil when there is no session yet; listVaults then reports every
	// vault as locked, which is exactly right for a first visit.
	writeJSON(w, http.StatusOK, map[string]any{"vaults": s.listVaults(b)})
}

type unlockRequest struct {
	Password string `json:"password"`
}

// handleUnlock opens a vault and keeps the session in memory for this browser.
func (s *Server) handleUnlock(w http.ResponseWriter, r *http.Request) {
	if !s.checkOrigin(w, r) {
		return
	}
	id := r.PathValue("id")
	vaultDir, ok := s.vaultPath(id)
	if !ok {
		writeError(w, http.StatusNotFound, "no such vault")
		return
	}

	// Throttle per vault and per client so scrypt cannot be used as a CPU
	// amplifier.
	if !s.limiter.allow(id + "|" + clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "too many unlock attempts; wait a minute and try again")
		return
	}

	var req unlockRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxJSONBody)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "malformed request body")
		return
	}
	if req.Password == "" {
		writeError(w, http.StatusBadRequest, "password is required")
		return
	}

	b, err := s.session(w, r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not start a session")
		return
	}

	sess, err := vault.OpenLocal(vaultDir, req.Password)
	if err != nil {
		s.cfg.Logger.Printf("unlock %s: %v", id, err)
		switch {
		case errors.Is(err, vault.ErrWrongPassword):
			writeError(w, http.StatusUnauthorized, "wrong password")
		case errors.Is(err, vault.ErrNoConfig):
			writeError(w, http.StatusNotFound, "not a gocryptfs vault")
		case errors.Is(err, vault.ErrPermission):
			writeError(w, http.StatusForbidden, "the server's OS user cannot read this vault (permission denied)")
		default:
			writeError(w, http.StatusInternalServerError, "could not unlock the vault")
		}
		return
	}

	// Opt in to the vault's diagnostics. They name plaintext paths, so the
	// package leaves the choice to the embedder; here they go to the server
	// log alongside everything else.
	sess.Logf = s.cfg.Logger.Printf

	thumbs, err := cache.NewBlob(s.cfg.ThumbCacheBytes)
	if err != nil {
		sess.Close()
		s.cfg.Logger.Printf("thumb cache for %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "could not unlock the vault")
		return
	}

	b.put(id, &unlockedVault{sess: sess, thumbs: thumbs})
	s.limiter.reset(id + "|" + clientIP(r))
	writeJSON(w, http.StatusOK, map[string]any{"vaults": s.listVaults(b)})
}

// handleLock closes a vault and wipes its keys and cached thumbnails.
func (s *Server) handleLock(w http.ResponseWriter, r *http.Request) {
	if !s.checkOrigin(w, r) {
		return
	}
	b, ok := s.existingSession(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "no session")
		return
	}
	b.lock(r.PathValue("id"))
	writeJSON(w, http.StatusOK, map[string]any{"vaults": s.listVaults(b)})
}

// handleLogout ends the browser session, locking every vault it held.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if !s.checkOrigin(w, r) {
		return
	}
	if c, err := r.Cookie(sessionCookieName); err == nil {
		s.sessions.destroy(c.Value)
	}
	s.clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// listResponse is what the browser view renders.
type listResponse struct {
	Path    string        `json:"path"`
	Parent  *string       `json:"parent"`
	Entries []vault.Entry `json:"entries"`
}

// handleList returns the decrypted contents of a directory.
func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	u, ok := s.requireVault(w, r)
	if !ok {
		return
	}
	p, ok := requestPath(w, r)
	if !ok {
		return
	}

	entries, err := u.sess.ListDir(p)
	if err != nil {
		s.writeVaultError(w, err, "list")
		return
	}

	resp := listResponse{Path: p, Entries: entries}
	if p != "" {
		parent := path.Dir(p)
		if parent == "." {
			parent = ""
		}
		resp.Parent = &parent
	}
	writeJSON(w, http.StatusOK, resp)
}

type mkdirRequest struct {
	Path string `json:"path"`
}

func (s *Server) handleMkdir(w http.ResponseWriter, r *http.Request) {
	if !s.checkOrigin(w, r) {
		return
	}
	u, ok := s.requireVault(w, r)
	if !ok {
		return
	}
	var req mkdirRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxJSONBody)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "malformed request body")
		return
	}
	if err := u.sess.Mkdir(req.Path); err != nil {
		s.writeVaultError(w, err, "mkdir")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type moveRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func (s *Server) handleMove(w http.ResponseWriter, r *http.Request) {
	if !s.checkOrigin(w, r) {
		return
	}
	u, ok := s.requireVault(w, r)
	if !ok {
		return
	}
	var req moveRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxJSONBody)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "malformed request body")
		return
	}
	if err := u.sess.Move(req.From, req.To); err != nil {
		s.writeVaultError(w, err, "move")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	if !s.checkOrigin(w, r) {
		return
	}
	u, ok := s.requireVault(w, r)
	if !ok {
		return
	}
	p, ok := requestPath(w, r)
	if !ok {
		return
	}
	if p == "" {
		writeError(w, http.StatusBadRequest, "refusing to delete the vault root")
		return
	}
	if err := u.sess.Remove(p); err != nil {
		s.writeVaultError(w, err, "delete")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleUpload streams a request body straight into the vault, encrypting as
// it goes — the plaintext is never held whole in memory or written to disk.
//
// The target directory comes from ?path= and the filename from ?name=.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if !s.checkOrigin(w, r) {
		return
	}
	u, ok := s.requireVault(w, r)
	if !ok {
		return
	}
	dir, ok := requestPath(w, r)
	if !ok {
		return
	}
	name := r.URL.Query().Get("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	// A filename is a single component; anything else is a traversal attempt.
	if strings.ContainsAny(name, "/\\") || name == "." || name == ".." {
		writeError(w, http.StatusBadRequest, "invalid file name")
		return
	}

	target := path.Join(dir, name)
	written, err := u.sess.Create(target, r.Body)
	if err != nil {
		s.writeVaultError(w, err, "upload")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": target, "size": written})
}

// requireVault resolves the request's vault, or writes the failure and
// returns false.
func (s *Server) requireVault(w http.ResponseWriter, r *http.Request) (*unlockedVault, bool) {
	id := r.PathValue("id")
	if _, ok := s.vaultPath(id); !ok {
		writeError(w, http.StatusNotFound, "no such vault")
		return nil, false
	}
	b, ok := s.existingSession(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "vault is locked")
		return nil, false
	}
	u, ok := b.unlocked(id)
	if !ok {
		writeError(w, http.StatusUnauthorized, "vault is locked")
		return nil, false
	}
	return u, true
}

// requestPath reads and validates the ?path= parameter.
func requestPath(w http.ResponseWriter, r *http.Request) (string, bool) {
	raw := r.URL.Query().Get("path")
	p, err := vault.CleanPath(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid path")
		return "", false
	}
	return p, true
}

// writeVaultError maps a vault error onto an HTTP status without leaking
// filesystem detail to the client.
func (s *Server) writeVaultError(w http.ResponseWriter, err error, op string) {
	// The client only sees the mapped message, but the server log gets the
	// full error — mapped failures are exactly the ones worth diagnosing when
	// a folder refuses to open.
	s.cfg.Logger.Printf("%s: %v", op, err)
	switch {
	case errors.Is(err, vault.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, vault.ErrExists):
		writeError(w, http.StatusConflict, "already exists")
	case errors.Is(err, vault.ErrInvalidPath):
		writeError(w, http.StatusBadRequest, "invalid path")
	case errors.Is(err, vault.ErrClosed):
		writeError(w, http.StatusUnauthorized, "vault is locked")
	case errors.Is(err, vault.ErrReadOnlyBackend):
		writeError(w, http.StatusForbidden, "vault is read-only")
	case errors.Is(err, vault.ErrPermission), errors.Is(err, fs.ErrPermission):
		writeError(w, http.StatusForbidden, "permission denied on the backend")
	default:
		writeError(w, http.StatusInternalServerError, op+" failed")
	}
}

// checkOrigin rejects cross-site state-changing requests.
//
// SameSite=Strict on the session cookie already blocks these in current
// browsers; this is the belt to that pair of braces, and covers clients that
// send the cookie from an unexpected origin.
func (s *Server) checkOrigin(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		// Non-browser clients (curl, scripts) send no Origin; there is no
		// ambient credential for them to be tricked into replaying.
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		writeError(w, http.StatusForbidden, "bad origin")
		return false
	}
	if u.Host != r.Host {
		writeError(w, http.StatusForbidden, "cross-origin request rejected")
		return false
	}
	return true
}

// clientIP extracts the peer address for rate limiting. It intentionally
// ignores X-Forwarded-For: this server is meant to be reached directly or
// through a trusted proxy, and honouring the header would let a client forge
// its way around the limiter.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

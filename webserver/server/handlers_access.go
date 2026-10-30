package server

import (
	"encoding/json"
	"io"
	"net/http"

	"golang.org/x/crypto/bcrypt"
)

// handleInfo tells a client which mode the server runs in, so it knows
// whether to send a vault password (server-decrypt) or an access password
// and decrypt itself (client-decrypt). It needs no session.
func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"mode": s.cfg.Mode, "version": s.cfg.Version})
}

type loginRequest struct {
	Password string `json:"password"`
}

// handleLogin checks the server access password in client-decrypt mode and
// marks the session logged in. That grants the raw endpoints: ciphertext and
// gocryptfs.conf, which without this gate would allow offline guessing of
// vault passwords.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.checkOrigin(w, r) {
		return
	}
	key := "login|" + clientIP(r)
	if !s.limiter.allow(key) {
		writeError(w, http.StatusTooManyRequests, "too many login attempts; wait a minute and try again")
		return
	}
	var req loginRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxJSONBody)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "malformed request body")
		return
	}
	if req.Password == "" {
		writeError(w, http.StatusBadRequest, "password is required")
		return
	}
	if bcrypt.CompareHashAndPassword(s.cfg.AccessPasswordHash, []byte(req.Password)) != nil {
		writeError(w, http.StatusUnauthorized, "wrong password")
		return
	}
	b, err := s.session(w, r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not start a session")
		return
	}
	b.setLoggedIn()
	s.limiter.reset(key)
	writeJSON(w, http.StatusOK, map[string]any{"vaults": s.listVaults(b)})
}

// requireRawAccess gates the raw endpoints: an unlocked vault in
// server-decrypt mode, a logged-in session in client-decrypt mode.
func (s *Server) requireRawAccess(w http.ResponseWriter, r *http.Request) bool {
	if s.cfg.Mode != ModeClientDecrypt {
		_, ok := s.requireVault(w, r)
		return ok
	}
	if _, ok := s.vaultPath(r.PathValue("id")); !ok {
		writeError(w, http.StatusNotFound, "no such vault")
		return false
	}
	b, ok := s.existingSession(r)
	if !ok || !b.isLoggedIn() {
		writeError(w, http.StatusUnauthorized, "login required")
		return false
	}
	return true
}

// HashAccessPassword returns the bcrypt hash to store in an access password
// file.
func HashAccessPassword(password string) ([]byte, error) {
	return bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
}

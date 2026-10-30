package server

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"sync"
	"time"

	"github.com/adrianm/imagethat/core/cache"
	"github.com/adrianm/imagethat/core/vault"
)

// sessionCookieName holds the browser session token. The token identifies a
// browser, not a vault — which vaults it has unlocked is server-side state.
const sessionCookieName = "imagethat_session"

// unlockedVault pairs an open vault session with the caches derived from it.
//
// The thumbnail cache is bound to the vault rather than to the server so that
// locking the vault also destroys its cache key, making every cached entry
// unreadable at that instant instead of lingering until eviction.
type unlockedVault struct {
	sess   *vault.Session
	thumbs *cache.Blob
}

func (u *unlockedVault) close() {
	if u.thumbs != nil {
		u.thumbs.Close()
	}
	u.sess.Close()
}

// browserSession is one browser's set of unlocked vaults.
//
// Unlocked vaults live here and nowhere else: no password, master key or
// plaintext is ever written to disk, so closing the process or expiring the
// session leaves nothing behind to recover.
type browserSession struct {
	token string

	mu       sync.Mutex
	vaults   map[string]*unlockedVault
	lastSeen time.Time
	// loggedIn is set by /api/login in client-decrypt mode.
	loggedIn bool
}

func (b *browserSession) setLoggedIn() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.loggedIn = true
}

func (b *browserSession) isLoggedIn() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.loggedIn
}

// unlocked returns the open vault for a vault ID, if any.
func (b *browserSession) unlocked(vaultID string) (*unlockedVault, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	u, ok := b.vaults[vaultID]
	return u, ok
}

// put stores an unlocked vault, closing any it replaces.
func (b *browserSession) put(vaultID string, u *unlockedVault) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if old, ok := b.vaults[vaultID]; ok {
		old.close()
	}
	b.vaults[vaultID] = u
}

// lock closes and forgets one vault, wiping both its crypto state and its
// cache key.
func (b *browserSession) lock(vaultID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	u, ok := b.vaults[vaultID]
	if !ok {
		return false
	}
	u.close()
	delete(b.vaults, vaultID)
	return true
}

// closeAll wipes every vault key this browser session held.
func (b *browserSession) closeAll() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, u := range b.vaults {
		u.close()
		delete(b.vaults, id)
	}
}

// unlockedIDs lists the vaults this session currently holds open.
func (b *browserSession) unlockedIDs() map[string]bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(map[string]bool, len(b.vaults))
	for id := range b.vaults {
		out[id] = true
	}
	return out
}

// sessionStore keeps browser sessions in memory and expires idle ones.
type sessionStore struct {
	idleTimeout time.Duration

	mu       sync.Mutex
	sessions map[string]*browserSession
}

func newSessionStore(idleTimeout time.Duration) *sessionStore {
	return &sessionStore{
		idleTimeout: idleTimeout,
		sessions:    make(map[string]*browserSession),
	}
}

// newToken returns a 256-bit random session token.
func newToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// create starts a new browser session.
func (st *sessionStore) create() (*browserSession, error) {
	token, err := newToken()
	if err != nil {
		return nil, err
	}
	b := &browserSession{
		token:    token,
		vaults:   make(map[string]*unlockedVault),
		lastSeen: time.Now(),
	}
	st.mu.Lock()
	st.sessions[token] = b
	st.mu.Unlock()
	return b, nil
}

// get returns a live session by token, refreshing its idle timer.
func (st *sessionStore) get(token string) (*browserSession, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	b, ok := st.sessions[token]
	if !ok {
		return nil, false
	}
	if st.idleTimeout > 0 && time.Since(b.lastSeen) > st.idleTimeout {
		delete(st.sessions, token)
		go b.closeAll()
		return nil, false
	}
	b.lastSeen = time.Now()
	return b, true
}

// destroy ends a session and wipes its vault keys.
func (st *sessionStore) destroy(token string) {
	st.mu.Lock()
	b, ok := st.sessions[token]
	delete(st.sessions, token)
	st.mu.Unlock()
	if ok {
		b.closeAll()
	}
}

// reapExpired closes sessions that have been idle past the timeout. An idle
// browser tab should not keep a vault's keys in memory indefinitely.
func (st *sessionStore) reapExpired() {
	if st.idleTimeout <= 0 {
		return
	}
	st.mu.Lock()
	var stale []*browserSession
	for token, b := range st.sessions {
		if time.Since(b.lastSeen) > st.idleTimeout {
			stale = append(stale, b)
			delete(st.sessions, token)
		}
	}
	st.mu.Unlock()

	for _, b := range stale {
		b.closeAll()
	}
}

// closeAll wipes every session's keys, for a clean shutdown.
func (st *sessionStore) closeAll() {
	st.mu.Lock()
	all := make([]*browserSession, 0, len(st.sessions))
	for token, b := range st.sessions {
		all = append(all, b)
		delete(st.sessions, token)
	}
	st.mu.Unlock()

	for _, b := range all {
		b.closeAll()
	}
}

// session returns the browser session for a request, creating one if needed
// and setting the cookie on the response.
func (s *Server) session(w http.ResponseWriter, r *http.Request) (*browserSession, error) {
	if c, err := r.Cookie(sessionCookieName); err == nil {
		if b, ok := s.sessions.get(c.Value); ok {
			return b, nil
		}
	}
	b, err := s.sessions.create()
	if err != nil {
		return nil, err
	}
	s.setSessionCookie(w, b.token)
	return b, nil
}

// existingSession returns the request's session without creating one.
func (s *Server) existingSession(r *http.Request) (*browserSession, bool) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return nil, false
	}
	return s.sessions.get(c.Value)
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:  sessionCookieName,
		Value: token,
		Path:  "/",
		// The session must not outlive the browser: vault access is not
		// something to resume silently days later.
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   s.cfg.TLS,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   s.cfg.TLS,
	})
}

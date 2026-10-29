package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestListVaultsCreatesNoSession checks that an unauthenticated request cannot
// allocate server-side state. Minting a session per cookie-less request let
// anyone who could reach the port grow the session map without bound.
func TestListVaultsCreatesNoSession(t *testing.T) {
	srv, _ := newTestServer(t)

	for i := 0; i < 25; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/vaults", nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		for _, ck := range rec.Result().Cookies() {
			if ck.Name == sessionCookieName && ck.Value != "" {
				t.Fatalf("GET /api/vaults set a session cookie")
			}
		}
	}

	srv.sessions.mu.Lock()
	n := len(srv.sessions.sessions)
	srv.sessions.mu.Unlock()
	if n != 0 {
		t.Errorf("%d sessions created by unauthenticated requests, want 0", n)
	}
}

// TestUnlockStillCreatesSession is the other half: /unlock must issue a cookie.
func TestUnlockStillCreatesSession(t *testing.T) {
	srv, _ := newTestServer(t)
	c := newClient(t, srv)
	id := c.vaultID()

	if c.cookie != nil {
		t.Fatal("a cookie was issued before unlocking")
	}
	c.unlock(id)
	if c.cookie == nil {
		t.Fatal("unlock did not issue a session cookie")
	}

	srv.sessions.mu.Lock()
	n := len(srv.sessions.sessions)
	srv.sessions.mu.Unlock()
	if n != 1 {
		t.Errorf("session count = %d, want 1", n)
	}
}

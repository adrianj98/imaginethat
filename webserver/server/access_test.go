package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

const testAccessPassword = "server-access-pw"

// newClientDecryptServer serves the test vault in client-decrypt mode.
func newClientDecryptServer(t *testing.T) (*Server, string) {
	t.Helper()
	// MinCost keeps the tests fast; the real hash uses the default cost.
	hash, err := bcrypt.GenerateFromPassword([]byte(testAccessPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return newTestServerWith(t, func(c *Config) {
		c.Mode = ModeClientDecrypt
		c.AccessPasswordHash = hash
		c.Version = "test"
	})
}

func (c *client) login(pw string) *httptest.ResponseRecorder {
	c.t.Helper()
	return c.json(http.MethodPost, "/api/login", loginRequest{Password: pw})
}

func TestModeConfigValidated(t *testing.T) {
	root := t.TempDir()
	if _, err := New(Config{Root: root, Mode: ModeClientDecrypt}); err == nil {
		t.Error("client-decrypt without an access password was accepted")
	}
	if _, err := New(Config{Root: root, AccessPasswordHash: []byte("x")}); err == nil {
		t.Error("an access password in server-decrypt mode was accepted")
	}
	if _, err := New(Config{Root: root, Mode: "bogus"}); err == nil {
		t.Error("an unknown mode was accepted")
	}
}

func TestInfoReportsMode(t *testing.T) {
	for _, tc := range []struct {
		srv  func(*testing.T) (*Server, string)
		want string
	}{
		{newTestServer, ModeServerDecrypt},
		{newClientDecryptServer, ModeClientDecrypt},
	} {
		srv, _ := tc.srv(t)
		rec := newClient(t, srv).do(http.MethodGet, "/api/info", nil)
		var info struct{ Mode string }
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &info) != nil || info.Mode != tc.want {
			t.Errorf("info = %d %s, want mode %s", rec.Code, rec.Body, tc.want)
		}
	}
}

func TestClientDecryptLogin(t *testing.T) {
	srv, _ := newClientDecryptServer(t)
	c := newClient(t, srv)

	if rec := c.do(http.MethodGet, "/api/vaults", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("vaults before login = %d, want 401", rec.Code)
	}
	if rec := c.login("wrong"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong access password = %d, want 401", rec.Code)
	}
	if rec := c.login(""); rec.Code != http.StatusBadRequest {
		t.Errorf("empty password = %d, want 400", rec.Code)
	}
	if rec := c.login(testAccessPassword); rec.Code != http.StatusOK {
		t.Fatalf("login = %d %s", rec.Code, rec.Body)
	}
	id := c.vaultID()

	// Other browsers still have no access.
	other := newClient(t, srv)
	if rec := other.do(http.MethodGet, "/api/vaults/"+id+"/raw/file?path=gocryptfs.conf", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("raw file without login = %d, want 401", rec.Code)
	}

	rec := c.do(http.MethodGet, "/api/vaults/"+id+"/raw/file?path=gocryptfs.conf", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "EncryptedKey") {
		t.Fatalf("raw conf = %d", rec.Code)
	}

	// Logging out revokes access.
	c.json(http.MethodPost, "/api/logout", nil)
	if rec := c.do(http.MethodGet, "/api/vaults/"+id+"/raw/list?path=", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("raw list after logout = %d, want 401", rec.Code)
	}
}

func TestClientDecryptHasNoDecryptingEndpoints(t *testing.T) {
	srv, _ := newClientDecryptServer(t)
	c := newClient(t, srv)
	c.login(testAccessPassword)
	id := c.vaultID()
	for _, ep := range []struct{ method, path string }{
		{http.MethodPost, "/unlock"},
		{http.MethodPost, "/lock"},
		{http.MethodGet, "/list?path="},
		{http.MethodGet, "/file?path=photo.jpg"},
		{http.MethodGet, "/thumb?path=photo.jpg&size=128"},
		{http.MethodPost, "/upload?path=&name=x"},
		{http.MethodPost, "/mkdir"},
		{http.MethodPost, "/move"},
		{http.MethodDelete, "/entry?path=photo.jpg"},
	} {
		rec := c.json(ep.method, "/api/vaults/"+id+ep.path, map[string]string{"password": fixturePassword})
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want it not to exist", ep.method, ep.path, rec.Code)
		}
	}
	if len(srv.sessions.sessions) != 1 {
		t.Fatalf("sessions = %d", len(srv.sessions.sessions))
	}
	for _, b := range srv.sessions.sessions {
		if len(b.unlockedIDs()) != 0 {
			t.Error("a vault was unlocked on a client-decrypt server")
		}
	}
}

func TestServerDecryptHasNoLogin(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := newClient(t, srv).login(testAccessPassword)
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("login on a server-decrypt server = %d", rec.Code)
	}
}

func TestLoginRateLimited(t *testing.T) {
	srv, _ := newClientDecryptServer(t)
	c := newClient(t, srv)
	var last int
	for i := 0; i < 15; i++ {
		last = c.login("wrong").Code
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("after 15 wrong logins status = %d, want 429", last)
	}
}

func TestLoginCrossOriginRejected(t *testing.T) {
	srv, _ := newClientDecryptServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"password":"`+testAccessPassword+`"}`))
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-origin login = %d, want 403", rec.Code)
	}
}

// The listing carries the directory IV and every long-name sidecar, so a
// client can decrypt a directory from one response.
func TestRawListCarriesDirIVAndLongNames(t *testing.T) {
	srv, root := newClientDecryptServer(t)
	c := newClient(t, srv)
	c.login(testAccessPassword)
	id := c.vaultID()

	rec := c.do(http.MethodGet, "/api/vaults/"+id+"/raw/list?path=", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("raw list = %d", rec.Code)
	}
	var resp rawListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	vaultDir := filepath.Join(root, "myvault")
	wantIV, _ := os.ReadFile(filepath.Join(vaultDir, dirIVName))
	if string(resp.DirIV) != string(wantIV) || len(wantIV) != 16 {
		t.Errorf("dirIV = %x, want %x", resp.DirIV, wantIV)
	}
	var longs int
	for _, e := range resp.Entries {
		if !strings.HasPrefix(e.Name, longNamePrefix) || strings.HasSuffix(e.Name, longNameSuffix) {
			continue
		}
		longs++
		want, _ := os.ReadFile(filepath.Join(vaultDir, e.Name+longNameSuffix))
		if resp.LongNames[e.Name] != string(want) || len(want) == 0 {
			t.Errorf("longNames[%s] = %q, want %q", e.Name, resp.LongNames[e.Name], want)
		}
	}
	if longs == 0 {
		t.Fatal("fixture has no long names to check")
	}
}

// /decrypted/ belongs to the browser's service worker. If a request reaches
// the server the worker is not running, and the SPA's HTML must not be handed
// to an <img> or <video> in its place.
func TestDecryptedPathsAreNotTheSPA(t *testing.T) {
	srv, _ := newClientDecryptServer(t)
	rec := newClient(t, srv).do(http.MethodGet, "/decrypted/abc/def?name=x.jpg", nil)
	if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "<html") {
		t.Errorf("/decrypted/ = %d %q, want a plain 404", rec.Code, rec.Body.String())
	}
}

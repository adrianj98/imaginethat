//go:build shimtest

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/adrianm/imagethat/webserver/server"
)

const shimAccessPassword = "access-pw"

// serveFixture runs the real imagethat-web server in process over a copy of
// the fixture vault, in client-decrypt mode or (clientDecrypt false)
// server-decrypt mode. It returns the vault directory, the server URL and the
// vault ID; paths records every request path the server saw.
func serveFixture(t *testing.T, clientDecrypt bool) (vaultDir, url, id string, paths *[]string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(fixtureDir, "gocryptfs.conf")); err != nil {
		t.Skip("fixture vault missing")
	}
	root := t.TempDir()
	vaultDir = filepath.Join(root, "v")
	if err := os.CopyFS(vaultDir, os.DirFS(fixtureDir)); err != nil {
		t.Fatal(err)
	}
	cfg := server.Config{Root: root, Logger: log.New(io.Discard, "", 0)}
	if clientDecrypt {
		hash, err := server.HashAccessPassword(shimAccessPassword)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Mode, cfg.AccessPasswordHash = server.ModeClientDecrypt, hash
	}
	srv, err := server.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	var mu sync.Mutex
	paths = new([]string)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		*paths = append(*paths, r.URL.Path)
		mu.Unlock()
		srv.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)

	hc := &http.Client{Jar: newJar(t)}
	if clientDecrypt {
		resp, err := hc.Post(ts.URL+"/api/login", "application/json",
			bytes.NewReader([]byte(`{"password":"`+shimAccessPassword+`"}`)))
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("login: %v %v", err, resp)
		}
		resp.Body.Close()
	}
	resp, err := hc.Get(ts.URL + "/api/vaults")
	if err != nil {
		t.Fatal(err)
	}
	var vs struct {
		Vaults []struct{ ID string } `json:"vaults"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&vs)
	resp.Body.Close()
	if len(vs.Vaults) != 1 {
		t.Fatalf("server found %d vaults", len(vs.Vaults))
	}
	mu.Lock()
	*paths = (*paths)[:0]
	mu.Unlock()
	return vaultDir, ts.URL, vs.Vaults[0].ID, paths
}

// TestOpenRemoteAgainstRealServer opens the fixture vault on a client-decrypt
// server through GocryptfsOpenRemote and GocryptfsOpenRemoteCached: the
// library logs in with the access password and decrypts here. The session
// must read exactly what the local session reads, including thumbnails, and
// refuse writes.
func TestOpenRemoteAgainstRealServer(t *testing.T) {
	vaultDir, url, id, _ := serveFixture(t, true)
	access := shimAccessPassword

	if m := tServerMode(url); m != 1 {
		t.Fatalf("GocryptfsServerMode = %d, want 1", m)
	}
	if rc := tOpenRemoteAccess(url, id, fixturePassword, "", "wrong"); rc != -7 {
		t.Errorf("wrong access password = %d, want -7", rc)
	}
	if rc := tOpenRemoteAccess(url, id, "wrong", "", access); rc != rcWrongPassword {
		t.Errorf("wrong password = %d, want %d", rc, rcWrongPassword)
	}
	if rc := tOpenRemoteAccess(url, "nope", fixturePassword, "", access); rc != rcNoConfig {
		t.Errorf("unknown vault = %d, want %d", rc, rcNoConfig)
	}
	if rc := tOpenRemoteAccess("not a url", id, fixturePassword, "", access); rc != rcCrypto {
		t.Errorf("bad URL = %d, want %d", rc, rcCrypto)
	}
	if rc := tOpenRemote(url, id, fixturePassword, ""); rc != rcLocked {
		t.Errorf("no access password = %d, want %d", rc, rcLocked)
	}

	rid := tOpenRemoteAccess(url, id, fixturePassword, "", access)
	if rid <= 0 {
		t.Fatalf("GocryptfsOpenRemote = %d", rid)
	}
	defer tClose(rid)
	lid := tOpen(vaultDir, fixturePassword)
	if lid <= 0 {
		t.Fatal(lid)
	}
	defer tClose(lid)

	rl, err1 := tList(rid, "")
	ll, err2 := tList(lid, "")
	if err1 != nil || err2 != nil || len(rl) != len(ll) || len(rl) == 0 {
		t.Fatalf("listing: remote %d (%v), local %d (%v)", len(rl), err1, len(ll), err2)
	}
	for _, e := range ll {
		if e.IsDir {
			continue
		}
		rb, errR := tDecryptFile(rid, e.Name)
		lb, errL := tDecryptFile(lid, e.Name)
		if errR != nil || errL != nil || !bytes.Equal(rb, lb) {
			t.Errorf("%s: remote %d bytes (%v), local %d (%v)", e.Name, len(rb), errR, len(lb), errL)
		}
		if chunk, n := tReadRange(rid, e.Name, 0, 4097); n >= 0 && !bytes.Equal(chunk, lb[:min(len(lb), 4097)]) {
			t.Errorf("%s: ReadRange differs over the remote session", e.Name)
		}
	}
	if jpg, err := tThumbGet(rid, "photo.jpg", 64); err != nil || len(jpg) < 2 || jpg[0] != 0xFF {
		t.Errorf("thumbnail over a remote session: %v (%d bytes)", err, len(jpg))
	}
	if rc := tMkdir(rid, "new"); rc != rcNoConfig {
		t.Errorf("Mkdir on a remote session = %d, want %d (read-only)", rc, rcNoConfig)
	}

	// The same vault through the network cache: caching photo.jpg stores its
	// ciphertext, and reads still match the local session.
	cacheDir := t.TempDir()
	if rc := tOpenRemoteCached(url, id, fixturePassword, "", "", cacheDir); rc != rcLocked {
		t.Errorf("cached, no access password = %d, want %d", rc, rcLocked)
	}
	cid := tOpenRemoteCached(url, id, fixturePassword, "", access, cacheDir)
	if cid <= 0 {
		t.Fatalf("GocryptfsOpenRemoteCached = %d", cid)
	}
	defer tClose(cid)
	if tIsCached(cid, "photo.jpg") {
		t.Fatal("cached before CacheFile")
	}
	j := tJobNew()
	defer tJobFree(j)
	if rc := tCacheFile(j, cid, "photo.jpg"); rc != 0 {
		t.Fatalf("CacheFile = %d", rc)
	}
	if !tIsCached(cid, "photo.jpg") || tNetworkCacheSize(cacheDir) == 0 {
		t.Fatal("CacheFile stored nothing")
	}
	lb, _ := tDecryptFile(lid, "photo.jpg")
	if cb, err := tDecryptFile(cid, "photo.jpg"); err != nil || !bytes.Equal(cb, lb) {
		t.Fatalf("cached read: %v, equal %v", err, bytes.Equal(cb, lb))
	}
	assertNoPlaintext(t, cacheDir, lb[:min(len(lb), 4096)])
	if rc := tUncache(cid, "photo.jpg"); rc != 0 || tIsCached(cid, "photo.jpg") {
		t.Fatalf("Uncache = %d", rc)
	}
	// Prefetch (how the viewer prebuffers the next item) fills the cache.
	if rc := tPrefetch(cid, "photo.jpg", 4096, 4096); rc != 0 {
		t.Fatalf("Prefetch = %d", rc)
	}
	if fr, err := tCachedRanges(cid, "photo.jpg"); err != nil || string(fr) == "[]" {
		t.Fatalf("CachedRanges after Prefetch = %s, %v", fr, err)
	}
	// A local session has nothing to prefetch.
	if rc := tPrefetch(lid, "photo.jpg", 4096, 4096); rc != 0 {
		t.Fatalf("Prefetch on a local session = %d", rc)
	}
}

// TestOpenRemoteRefusesServerDecrypt: the library cannot open a vault on a
// server-decrypt server, and the server never sees the vault password: no
// call reaches its /unlock endpoint.
func TestOpenRemoteRefusesServerDecrypt(t *testing.T) {
	_, url, id, paths := serveFixture(t, false)

	if rc := tOpenRemote(url, id, fixturePassword, ""); rc != rcLocked {
		t.Errorf("GocryptfsOpenRemote without access password = %d, want %d", rc, rcLocked)
	}
	if rc := tOpenRemoteCached(url, id, fixturePassword, "", "", t.TempDir()); rc != rcLocked {
		t.Errorf("GocryptfsOpenRemoteCached without access password = %d, want %d", rc, rcLocked)
	}
	if rc := tOpenRemoteAccess(url, id, fixturePassword, "", shimAccessPassword); rc > 0 {
		tClose(rc)
		t.Error("GocryptfsOpenRemote opened a server-decrypt vault")
	}
	h := tServerNew(url)
	defer tServerFree(h)
	if rc := tServerOpenVault(h, id, fixturePassword, ""); rc != rcLocked {
		t.Errorf("GocryptfsServerOpenVault without login = %d, want %d", rc, rcLocked)
	}
	for _, p := range *paths {
		if strings.HasSuffix(p, "/unlock") {
			t.Errorf("the library called %s", p)
		}
	}
}

func TestServerModeOfServerDecrypt(t *testing.T) {
	srv, err := server.New(server.Config{Root: t.TempDir(), Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv)
	defer ts.Close()
	if m := tServerMode(ts.URL); m != 0 {
		t.Errorf("GocryptfsServerMode = %d, want 0", m)
	}
}

func newJar(t *testing.T) http.CookieJar {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return jar
}

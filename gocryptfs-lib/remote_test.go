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
	"testing"

	"github.com/adrianm/imagethat/webserver/server"
)

// TestOpenRemoteAgainstRealServer runs the real imagethat-web server in
// process over a copy of the fixture vault and opens it through
// GocryptfsOpenRemote: the session must read exactly what the local session
// reads, including thumbnails, and refuse writes.
func TestOpenRemoteAgainstRealServer(t *testing.T) {
	if _, err := os.Stat(filepath.Join(fixtureDir, "gocryptfs.conf")); err != nil {
		t.Skip("fixture vault missing")
	}
	root := t.TempDir()
	if err := os.CopyFS(filepath.Join(root, "v"), os.DirFS(fixtureDir)); err != nil {
		t.Fatal(err)
	}
	srv, err := server.New(server.Config{Root: root, Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/vaults")
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
	id := vs.Vaults[0].ID

	if rc := tOpenRemote(ts.URL, id, "wrong", ""); rc != rcWrongPassword {
		t.Errorf("wrong password = %d, want %d", rc, rcWrongPassword)
	}
	if rc := tOpenRemote(ts.URL, "nope", fixturePassword, ""); rc != rcNoConfig {
		t.Errorf("unknown vault = %d, want %d", rc, rcNoConfig)
	}
	if rc := tOpenRemote("not a url", id, fixturePassword, ""); rc != rcCrypto {
		t.Errorf("bad URL = %d, want %d", rc, rcCrypto)
	}

	rid := tOpenRemote(ts.URL, id, fixturePassword, "")
	if rid <= 0 {
		t.Fatalf("GocryptfsOpenRemote = %d", rid)
	}
	defer tClose(rid)
	lid := tOpen(filepath.Join(root, "v"), fixturePassword)
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

	// The same vault with a file cache: caching photo.jpg stores its
	// ciphertext, and reads still match the local session.
	cacheDir := t.TempDir()
	cid := tOpenRemoteCached(ts.URL, id, fixturePassword, "", "", cacheDir)
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
	if !tIsCached(cid, "photo.jpg") || tFileCacheSize(cacheDir) == 0 {
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
}

// TestOpenRemoteClientDecrypt opens a vault on a client-decrypt server: the
// library logs in with the access password and unlocks the vault itself.
func TestOpenRemoteClientDecrypt(t *testing.T) {
	if _, err := os.Stat(filepath.Join(fixtureDir, "gocryptfs.conf")); err != nil {
		t.Skip("fixture vault missing")
	}
	root := t.TempDir()
	if err := os.CopyFS(filepath.Join(root, "v"), os.DirFS(fixtureDir)); err != nil {
		t.Fatal(err)
	}
	hash, err := server.HashAccessPassword("access-pw")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := server.New(server.Config{
		Root: root, Logger: log.New(io.Discard, "", 0),
		Mode: server.ModeClientDecrypt, AccessPasswordHash: hash,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv)
	defer ts.Close()

	if m := tServerMode(ts.URL); m != 1 {
		t.Fatalf("GocryptfsServerMode = %d, want 1", m)
	}
	// The vault ID is the same hash the server-decrypt test sees; get it
	// through a logged-in request.
	jar := &http.Client{Jar: newJar(t)}
	resp, err := jar.Post(ts.URL+"/api/login", "application/json", bytes.NewReader([]byte(`{"password":"access-pw"}`)))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %v %v", err, resp)
	}
	var vs struct {
		Vaults []struct{ ID string } `json:"vaults"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&vs)
	resp.Body.Close()
	if len(vs.Vaults) != 1 {
		t.Fatalf("server found %d vaults", len(vs.Vaults))
	}
	id := vs.Vaults[0].ID

	if rc := tOpenRemoteAccess(ts.URL, id, fixturePassword, "", "wrong"); rc != -7 {
		t.Errorf("wrong access password = %d, want -7", rc)
	}
	if rc := tOpenRemoteAccess(ts.URL, id, "wrong", "", "access-pw"); rc != rcWrongPassword {
		t.Errorf("wrong vault password = %d, want %d", rc, rcWrongPassword)
	}
	rid := tOpenRemoteAccess(ts.URL, id, fixturePassword, "", "access-pw")
	if rid <= 0 {
		t.Fatalf("GocryptfsOpenRemote = %d", rid)
	}
	defer tClose(rid)
	lid := tOpen(filepath.Join(root, "v"), fixturePassword)
	if lid <= 0 {
		t.Fatal(lid)
	}
	defer tClose(lid)
	rb, errR := tDecryptFile(rid, "photo.jpg")
	lb, errL := tDecryptFile(lid, "photo.jpg")
	if errR != nil || errL != nil || !bytes.Equal(rb, lb) {
		t.Errorf("photo.jpg: remote %d bytes (%v), local %d (%v)", len(rb), errR, len(lb), errL)
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

//go:build shimtest

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adrianm/imagethat/core/drive"
	"github.com/adrianm/imagethat/core/drive/drivetest"
	"github.com/adrianm/imagethat/webserver/server"
)

// A Drive vault through the C API: sign in, create, write through a session
// like a local vault, read back, cache, and plain listings.
func TestDriveThroughShim(t *testing.T) {
	srv := drivetest.New()
	defer srv.Close()
	setTestDriveConfig(func(c *drive.Config) {
		fake := srv.Config()
		c.APIBase, c.TokenURL, c.AuthURL = fake.APIBase, fake.TokenURL, fake.AuthURL
	})
	defer setTestDriveConfig(nil)

	h := tDriveNew("")
	defer tDriveFree(h)
	if _, err := tDriveList(h, "root", false); err == nil || !strings.HasPrefix(err.Error(), "not_connected:") {
		t.Fatalf("list before sign-in: %v", err)
	}

	// Sign in: the "browser" follows the redirect with Google's code.
	authURL := tDriveSignInStart(h)
	u, err := url.Parse(authURL)
	if err != nil || authURL == "" {
		t.Fatalf("sign-in URL %q", authURL)
	}
	q := u.Query()
	go func() {
		resp, err := http.Get(q.Get("redirect_uri") + "?code=good-code&state=" + url.QueryEscape(q.Get("state")))
		if err == nil {
			resp.Body.Close()
		}
	}()
	if rc := tDriveSignInWait(h); rc != 0 {
		t.Fatalf("SignInWait = %d", rc)
	}
	if tDriveRefreshToken(h) == "" {
		t.Fatal("no refresh token")
	}
	tDriveSignInStart(h)
	tDriveSignInCancel(h)
	if rc := tDriveSignInWait(h); rc != rcCancelled {
		t.Fatalf("cancelled sign-in = %d", rc)
	}

	folder := srv.AddFolder("root", "Vault")
	if rc := tInitDrive(h, folder, "pw"); rc != 0 {
		t.Fatalf("InitDrive = %d", rc)
	}
	if rc := tOpenDrive(h, folder, "wrong", ""); rc != rcWrongPassword {
		t.Fatalf("wrong password = %d", rc)
	}
	cacheDir := t.TempDir()
	id := tOpenDrive(h, folder, "pw", cacheDir)
	if id <= 0 {
		t.Fatalf("OpenDrive = %d", id)
	}
	defer tClose(id)

	want := pattern(9, 300_000)
	if rc := tMkdir(id, "Trip"); rc != 0 {
		t.Fatalf("Mkdir = %d", rc)
	}
	w := tCreate(id, "Trip/clip.mov")
	tWrite(w, want[:100_000])
	tWrite(w, want[100_000:])
	if rc := tWriterClose(w); rc != 0 {
		t.Fatalf("WriterClose = %d", rc)
	}
	got, err := tDecryptFile(id, "Trip/clip.mov")
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("read back: %v", err)
	}
	if chunk, n := tReadRange(id, "Trip/clip.mov", 4000, 5000); n != 5000 || !bytes.Equal(chunk, want[4000:9000]) {
		t.Fatalf("ReadRange = %d", n)
	}
	if rc := tRename(id, "Trip/clip.mov", ""); rc != 0 {
		t.Fatalf("Rename = %d", rc)
	}

	j := tJobNew()
	defer tJobFree(j)
	if rc := tCacheFile(j, id, "clip.mov"); rc != 0 || !tIsCached(id, "clip.mov") {
		t.Fatalf("CacheFile = %d", rc)
	}
	if fr, err := tCachedRanges(id, "clip.mov"); err != nil || string(fr) != "[[0,1]]" {
		t.Fatalf("CachedRanges = %s, %v", fr, err)
	}
	if tNetworkCacheSize(cacheDir) == 0 {
		t.Fatal("Drive cache empty")
	}
	// Nothing in plaintext on Drive or in the cache.
	for _, n := range srv.Children(folder) {
		if strings.Contains(n, "Trip") || strings.Contains(n, "clip") {
			t.Fatalf("plaintext name on Drive: %s", n)
		}
	}
	assertNoPlaintext(t, cacheDir, want[:4096])

	js, err := tDriveFindVaultRoot(h, folder)
	if err != nil || !strings.Contains(string(js), folder) {
		t.Fatalf("FindVaultRoot = %s, %v", js, err)
	}
	if _, err := tDriveFindVaultRoot(h, "root"); err == nil || !strings.HasPrefix(err.Error(), "not_found:") {
		t.Fatalf("My Drive: %v", err)
	}
	if js, err := tDriveList(h, "root", true); err != nil || !strings.Contains(string(js), `"isFolder":true`) {
		t.Fatalf("plain listing: %s, %v", js, err)
	}
}

// A client-decrypt server through a server handle: log in, list vaults,
// open one here. Without a login the vault password is never sent.
func TestServerHandleClientDecrypt(t *testing.T) {
	if _, err := os.Stat(filepath.Join(fixtureDir, "gocryptfs.conf")); err != nil {
		t.Skip("fixture vault missing")
	}
	root := t.TempDir()
	if err := os.CopyFS(filepath.Join(root, "v"), os.DirFS(fixtureDir)); err != nil {
		t.Fatal(err)
	}
	hash, err := server.HashAccessPassword("access")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := server.New(server.Config{Root: root, Mode: server.ModeClientDecrypt, AccessPasswordHash: hash,
		Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv)
	defer ts.Close()

	h := tServerNew(ts.URL)
	if h <= 0 {
		t.Fatalf("ServerNew = %d", h)
	}
	defer tServerFree(h)
	if m := tServerGetMode(h); m != 1 {
		t.Fatalf("mode = %d", m)
	}
	if _, err := tServerVaults(h); err == nil || !strings.HasPrefix(err.Error(), "locked:") {
		t.Fatalf("vaults before login: %v", err)
	}
	if rc := tServerLogin(h, "wrong"); rc != rcCancelled {
		t.Fatalf("wrong access password = %d", rc)
	}
	if rc := tServerLogin(h, "access"); rc != 0 {
		t.Fatalf("Login = %d", rc)
	}
	js, err := tServerVaults(h)
	var vaults []struct{ ID string }
	if err != nil || json.Unmarshal(js, &vaults) != nil || len(vaults) != 1 {
		t.Fatalf("Vaults = %s, %v", js, err)
	}
	id := tServerOpenVault(h, vaults[0].ID, fixturePassword, t.TempDir())
	if id <= 0 {
		t.Fatalf("ServerOpenVault = %d", id)
	}
	defer tClose(id)
	lid := tOpen(filepath.Join(root, "v"), fixturePassword)
	defer tClose(lid)
	a, err1 := tDecryptFile(id, "photo.jpg")
	b, err2 := tDecryptFile(lid, "photo.jpg")
	if err1 != nil || err2 != nil || !bytes.Equal(a, b) {
		t.Fatalf("photo.jpg differs: %v %v", err1, err2)
	}

	h2 := tServerNew(ts.URL)
	defer tServerFree(h2)
	if id := tServerOpenVault(h2, vaults[0].ID, fixturePassword, ""); id > 0 {
		tClose(id)
		t.Fatal("opened without logging in")
	}
}

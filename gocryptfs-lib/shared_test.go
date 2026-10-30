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
	if tDriveCacheSize(cacheDir) == 0 {
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

// The server-decrypt API through the C API, against the real server.
func TestServerAPIThroughShim(t *testing.T) {
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

	h := tServerNew(ts.URL)
	if h <= 0 {
		t.Fatalf("ServerNew = %d", h)
	}
	defer tServerFree(h)
	js, err := tServerVaults(h)
	var vaults []struct{ ID string }
	if err != nil || json.Unmarshal(js, &vaults) != nil || len(vaults) != 1 {
		t.Fatalf("Vaults = %s, %v", js, err)
	}
	id := vaults[0].ID
	if _, err := tServerList(h, id, ""); err == nil || !strings.HasPrefix(err.Error(), "locked:") {
		t.Fatalf("list while locked: %v", err)
	}
	if rc := tServerUnlock(h, id, "wrong"); rc != rcWrongPassword {
		t.Fatalf("wrong password = %d", rc)
	}
	if rc := tServerUnlock(h, id, fixturePassword); rc != 0 {
		t.Fatalf("Unlock = %d", rc)
	}
	if js, err := tServerList(h, id, ""); err != nil || !strings.Contains(string(js), "photo.jpg") {
		t.Fatalf("List = %s, %v", js, err)
	}

	data := pattern(3, 200_000)
	w := tServerUploadBegin(h, id, "", "up.bin", int64(len(data)))
	tWrite(w, data[:50_000])
	tWrite(w, data[50_000:])
	if rc := tWriterClose(w); rc != 0 {
		t.Fatalf("upload = %d", rc)
	}
	if n := tServerFileSize(h, id, "up.bin"); n != int64(len(data)) {
		t.Fatalf("size = %d", n)
	}
	if b, n := tServerReadRange(h, id, "up.bin", 70_000, 4096); n != 4096 || !bytes.Equal(b, data[70_000:74_096]) {
		t.Fatalf("ReadRange = %d", n)
	}
	if rc := tServerMove(h, id, "up.bin", "moved.bin"); rc != 0 {
		t.Fatalf("Move = %d", rc)
	}
	if n := tServerFileSize(h, id, "up.bin"); n != rcNoConfig {
		t.Fatalf("old name size = %d", n)
	}
	if rc := tServerDelete(h, id, "moved.bin"); rc != 0 {
		t.Fatalf("Delete = %d", rc)
	}
	// The upload's plaintext is nowhere in the vault directory.
	assertNoPlaintext(t, filepath.Join(root, "v"), data[:4096])
}

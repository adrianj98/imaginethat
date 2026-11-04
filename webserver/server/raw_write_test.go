package server

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adrianm/imagethat/core/cache"
	"github.com/adrianm/imagethat/core/vault"
)

// openLocal opens the server's copy of the vault directly from disk, a
// second opinion on what the remote client wrote.
func openLocal(t *testing.T, srv *Server) *vault.Session {
	t.Helper()
	s, err := vault.OpenLocal(filepath.Join(srv.cfg.Root, "myvault"), fixturePassword)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// exerciseWrites runs every kind of write through a remote session and checks
// each against a local session on the server's files.
func exerciseWrites(t *testing.T, srv *Server, rs *vault.Session) {
	t.Helper()
	// Several blocks plus a partial one, a long name (hashed into a sidecar)
	// and an empty file.
	big := bytes.Repeat([]byte("0123456789abcdef"), 4096*3/16+100)
	longName := strings.Repeat("a long file name ", 12) + ".jpg"
	files := map[string][]byte{
		"uploaded/big.bin":     big,
		"uploaded/" + longName: []byte("long"),
		"uploaded/empty.txt":   nil,
	}

	if err := rs.Mkdir("uploaded"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	for p, data := range files {
		if _, err := rs.Create(p, bytes.NewReader(data)); err != nil {
			t.Fatalf("Create %s: %v", p, err)
		}
	}
	if _, err := rs.Create("uploaded/big.bin", bytes.NewReader([]byte("x"))); !errors.Is(err, vault.ErrExists) {
		t.Errorf("Create over an existing file: %v, want ErrExists", err)
	}

	local := openLocal(t, srv)
	for p, data := range files {
		got, err := local.ReadFile(p)
		if err != nil || !bytes.Equal(got, data) {
			t.Errorf("%s read locally: %d bytes (%v), want %d", p, len(got), err, len(data))
		}
		got, err = rs.ReadFile(p)
		if err != nil || !bytes.Equal(got, data) {
			t.Errorf("%s read back remotely: %d bytes (%v), want %d", p, len(got), err, len(data))
		}
	}

	if _, err := rs.Replace("uploaded/big.bin", bytes.NewReader([]byte("replaced"))); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if err := rs.Move("uploaded/big.bin", "moved.bin"); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if got, err := rs.ReadFile("moved.bin"); err != nil || string(got) != "replaced" {
		t.Errorf("after Replace and Move: %q, %v", got, err)
	}
	if got, err := openLocal(t, srv).ReadFile("moved.bin"); err != nil || string(got) != "replaced" {
		t.Errorf("after Replace and Move, locally: %q, %v", got, err)
	}

	if err := rs.Remove("uploaded"); err != nil {
		t.Fatalf("Remove dir: %v", err)
	}
	if err := rs.Remove("moved.bin"); err != nil {
		t.Fatalf("Remove file: %v", err)
	}
	if _, err := openLocal(t, srv).Stat("uploaded"); !errors.Is(err, vault.ErrNotFound) {
		t.Errorf("uploaded/ still there: %v", err)
	}

	// No temp files left behind, ours or the server's.
	_ = filepath.Walk(filepath.Join(srv.cfg.Root, "myvault"), func(p string, info os.FileInfo, err error) error {
		if err == nil && strings.HasPrefix(info.Name(), ".imagethat-") {
			t.Errorf("leftover temp file %s", p)
		}
		return nil
	})
}

func TestRawModeWrites(t *testing.T) {
	srv, _, id, rc, _, _ := rawSetup(t)
	rs, err := rc.OpenVault(context.Background(), id, fixturePassword)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	exerciseWrites(t, srv, rs)
}

func TestRawModeWritesThroughCache(t *testing.T) {
	srv, _, id, rc, _, _ := rawSetup(t)
	ranges, err := cache.NewRanges(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	rs, err := rc.OpenVaultCached(context.Background(), id, fixturePassword, ranges)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	exerciseWrites(t, srv, rs)
}

func TestRawWritesLogInAgainAfterServerExpiry(t *testing.T) {
	srv, _, id, rc, _, _ := rawSetup(t)
	rs, err := rc.OpenVault(context.Background(), id, fixturePassword)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	srv.sessions.closeAll()
	if _, err := rs.Create("after-expiry.txt", strings.NewReader("hi")); err != nil {
		t.Fatalf("Create after the server dropped the session: %v", err)
	}
}

func TestRawWriteEndpointsRefuse(t *testing.T) {
	srv, root := newClientDecryptServer(t)
	c := newClient(t, srv)
	c.login(testAccessPassword)
	id := c.vaultID()
	base := "/api/vaults/" + id + "/raw/"

	// Not logged in.
	anon := newClient(t, srv)
	if rec := anon.do(http.MethodPut, base+"file?path=x", strings.NewReader("x")); rec.Code != http.StatusUnauthorized {
		t.Errorf("PUT without login: %d, want 401", rec.Code)
	}

	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "myvault", "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "victim"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "victim"), filepath.Join(root, "myvault", "link")); err != nil {
		t.Fatal(err)
	}
	conf, _ := os.ReadFile(filepath.Join(root, "myvault", "gocryptfs.conf"))

	for _, tc := range []struct {
		method, target string
		body           string
	}{
		{http.MethodPut, "file?path=gocryptfs.conf", "x"},
		{http.MethodDelete, "entry?path=gocryptfs.conf", ""},
		{http.MethodPut, "file?path=../x", "x"},
		{http.MethodPut, "file?path=escape/x", "x"},
		{http.MethodPut, "file?path=link", "x"},
		{http.MethodPost, "mkdir?path=escape/d", ""},
		{http.MethodDelete, "entry?path=escape", ""},
		{http.MethodDelete, "entry?path=", ""},
		{http.MethodPost, "rename", `{"from":"gocryptfs.conf","to":"x"}`},
		{http.MethodPost, "rename", `{"from":"gocryptfs.diriv","to":"escape/x"}`},
	} {
		rec := c.do(tc.method, base+tc.target, strings.NewReader(tc.body))
		if rec.Code == http.StatusOK {
			t.Errorf("%s %s %s was allowed", tc.method, tc.target, tc.body)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(root, "myvault", "gocryptfs.conf")); !bytes.Equal(got, conf) {
		t.Error("gocryptfs.conf changed")
	}
	if got, _ := os.ReadFile(filepath.Join(outside, "victim")); string(got) != "keep" {
		t.Error("a write went through a symlink out of the vault")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 1 {
		t.Errorf("files appeared outside the vault: %d entries", len(entries))
	}

	// Cross-origin writes are rejected like every other mutation.
	req := httptest.NewRequest(http.MethodPut, base+"file?path=x", strings.NewReader("x"))
	req.Header.Set("Origin", "https://evil.example")
	req.AddCookie(c.cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-origin PUT: %d, want 403", rec.Code)
	}
}

func TestServerDecryptHasNoRawWrites(t *testing.T) {
	srv, _ := newTestServer(t)
	c := newClient(t, srv)
	id := c.vaultID()
	c.unlock(id)
	base := "/api/vaults/" + id + "/raw/"
	for _, tc := range []struct{ method, target string }{
		{http.MethodPut, "file?path=x"},
		{http.MethodPost, "mkdir?path=x"},
		{http.MethodPost, "rename"},
		{http.MethodDelete, "entry?path=x"},
	} {
		rec := c.do(tc.method, base+tc.target, strings.NewReader("{}"))
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s on a server-decrypt server: %d, want it not to exist", tc.method, tc.target, rec.Code)
		}
	}
}

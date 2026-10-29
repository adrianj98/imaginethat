package server

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/adrianm/imagethat/core/remote"
	"github.com/adrianm/imagethat/core/vault"
)

// countingTransport counts requests, to check reads are coalesced over HTTP.
type countingTransport struct{ n atomic.Int64 }

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.n.Add(1)
	return http.DefaultTransport.RoundTrip(r)
}

// rawSetup serves the test vault over a real listener and returns the vault
// ID, a remote client, and a local session on the same files for comparison.
func rawSetup(t *testing.T) (*Server, *httptest.Server, string, *remote.Client, *countingTransport, *vault.Session) {
	t.Helper()
	srv, root := newTestServer(t)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	id := newClient(t, srv).vaultID()

	ct := &countingTransport{}
	rc, err := remote.New(ts.URL, remote.Options{Transport: ct})
	if err != nil {
		t.Fatal(err)
	}
	local, err := vault.OpenLocal(filepath.Join(root, "myvault"), fixturePassword)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { local.Close() })
	return srv, ts, id, rc, ct, local
}

// walk lists every file path in a session, recursively.
func walk(t *testing.T, s *vault.Session, dir string) []string {
	t.Helper()
	entries, err := s.ListDir(dir)
	if err != nil {
		t.Fatalf("ListDir(%q): %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		p := path.Join(dir, e.Name)
		if e.IsDir {
			out = append(out, walk(t, s, p)...)
		} else {
			out = append(out, p)
		}
	}
	return out
}

func TestRawModeMatchesLocalDecryption(t *testing.T) {
	_, _, id, rc, _, local := rawSetup(t)
	ctx := context.Background()

	if _, err := rc.OpenVault(ctx, id, "not the password"); !errors.Is(err, remote.ErrWrongPassword) {
		t.Fatalf("wrong password: %v", err)
	}
	if _, err := rc.OpenVault(ctx, "nope", fixturePassword); !errors.Is(err, remote.ErrNotFound) {
		t.Errorf("unknown vault: %v", err)
	}

	rs, err := rc.OpenVault(ctx, id, fixturePassword)
	if err != nil {
		t.Fatalf("OpenVault: %v", err)
	}
	defer rs.Close()

	want := walk(t, local, "")
	got := walk(t, rs, "")
	if len(got) != len(want) || len(want) == 0 {
		t.Fatalf("remote lists %d files, local %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("file %d: remote %q, local %q", i, got[i], want[i])
		}
	}
	// Every file, long names and nested directories included, decrypts to
	// the same bytes as the local session.
	for _, p := range want {
		lb, err1 := local.ReadFile(p)
		rb, err2 := rs.ReadFile(p)
		if err1 != nil || err2 != nil || !bytes.Equal(lb, rb) {
			t.Errorf("%s: local %d bytes (%v), remote %d bytes (%v)", p, len(lb), err1, len(rb), err2)
		}
	}

	if _, err := rs.ReadFile("missing.jpg"); !errors.Is(err, vault.ErrNotFound) {
		t.Errorf("missing file: %v, want vault.ErrNotFound", err)
	}
	// Writes go through the decrypted API; a raw-mode session refuses them.
	if err := rs.Mkdir("new"); !errors.Is(err, vault.ErrReadOnlyBackend) {
		t.Errorf("Mkdir over raw mode: %v, want ErrReadOnlyBackend", err)
	}
}

func TestRawModeReadsAreCoalesced(t *testing.T) {
	_, _, id, rc, ct, local := rawSetup(t)
	rs, err := rc.OpenVault(context.Background(), id, fixturePassword)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()

	f, err := rs.Open("large.bin")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	before := ct.n.Load()
	buf := make([]byte, f.Size())
	if _, err := f.ReadAt(buf, 0); err != nil {
		t.Fatal(err)
	}
	if reqs := ct.n.Load() - before; reqs != 1 {
		t.Errorf("reading %d bytes took %d HTTP requests, want 1", len(buf), reqs)
	}
	want, _ := local.ReadFile("large.bin")
	if !bytes.Equal(buf, want) {
		t.Error("large.bin differs from the local decryption")
	}
}

func TestRawModeReunlocksAfterServerExpiry(t *testing.T) {
	srv, _, id, rc, _, local := rawSetup(t)
	rs, err := rc.OpenVault(context.Background(), id, fixturePassword)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()

	// The server's idle reaper (or a restart) drops every session.
	srv.sessions.closeAll()

	got, err := rs.ReadFile("photo.jpg")
	if err != nil {
		t.Fatalf("read after the server dropped the session: %v", err)
	}
	want, _ := local.ReadFile("photo.jpg")
	if !bytes.Equal(got, want) {
		t.Error("content differs after re-unlock")
	}
}

func TestRawEndpointsNeedUnlockAndStayInVault(t *testing.T) {
	srv, root := newTestServer(t)
	c := newClient(t, srv)
	id := c.vaultID()

	// No session: nothing.
	for _, target := range []string{
		"/api/vaults/" + id + "/raw/file?path=gocryptfs.conf",
		"/api/vaults/" + id + "/raw/list?path=",
	} {
		if rec := c.do(http.MethodGet, target, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without unlock: %d, want 401", target, rec.Code)
		}
	}

	c.unlock(id)
	rec := c.do(http.MethodGet, "/api/vaults/"+id+"/raw/file?path=gocryptfs.conf", nil)
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte("EncryptedKey")) {
		t.Fatalf("conf after unlock: %d", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc == "" {
		t.Error("raw responses must not be cacheable by browsers or proxies")
	}

	// Escapes: "..", an absolute-looking path, and a symlink planted in the
	// vault pointing outside it.
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("outside the vault"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "myvault", "escape")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"../myvault/gocryptfs.conf", "/../../etc/passwd", "escape"} {
		rec := c.do(http.MethodGet, "/api/vaults/"+id+"/raw/file?path="+p, nil)
		if rec.Code == http.StatusOK {
			t.Errorf("raw path %q was served (%q)", p, rec.Body.String())
		}
	}
	if rec := c.do(http.MethodGet, "/api/vaults/"+id+"/raw/list?path=..", nil); rec.Code == http.StatusOK {
		t.Error("raw list of .. was served")
	}

	// Range works on raw files.
	req := httptest.NewRequest(http.MethodGet, "/api/vaults/"+id+"/raw/file?path=gocryptfs.conf", nil)
	req.AddCookie(c.cookie)
	req.Header.Set("Range", "bytes=0-9")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusPartialContent || rr.Body.Len() != 10 {
		t.Errorf("Range on raw file: %d, %d bytes", rr.Code, rr.Body.Len())
	}
}

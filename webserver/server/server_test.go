package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/adrianm/imagethat/core/vault"
)

const (
	fixturePassword = "test-password-123"
	fixtureSrc      = "../vault/testdata/vault"
)

// patternBytes mirrors the generator in the vault package's fixture so tests
// here can predict file contents.
func patternBytes(seed uint64, n int) []byte {
	out := make([]byte, 0, n+sha256.Size)
	block := make([]byte, 8)
	binary.LittleEndian.PutUint64(block, seed)
	h := sha256.Sum256(block)
	for len(out) < n {
		out = append(out, h[:]...)
		h = sha256.Sum256(h[:])
	}
	return out[:n]
}

// newTestServer builds a server over a private copy of the test vault, placed
// in a subdirectory so vault discovery has something to find.
func newTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(fixtureSrc, "gocryptfs.conf")); err != nil {
		t.Skipf("test vault missing at %s", fixtureSrc)
	}

	root := t.TempDir()
	dst := filepath.Join(root, "myvault")
	copyTree(t, fixtureSrc, dst)

	srv, err := New(Config{
		Root:            root,
		ScanDepth:       3,
		ThumbCacheBytes: 8 << 20,
		Logger:          log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(srv.Close)
	return srv, root
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	})
	if err != nil {
		t.Fatalf("copy %s: %v", src, err)
	}
}

// client drives the server through httptest, keeping the session cookie.
type client struct {
	t      *testing.T
	srv    *Server
	cookie *http.Cookie
}

func newClient(t *testing.T, srv *Server) *client { return &client{t: t, srv: srv} }

func (c *client) do(method, target string, body io.Reader) *httptest.ResponseRecorder {
	c.t.Helper()
	req := httptest.NewRequest(method, target, body)
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	rec := httptest.NewRecorder()
	c.srv.ServeHTTP(rec, req)
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == sessionCookieName && ck.Value != "" {
			c.cookie = ck
		}
	}
	return rec
}

func (c *client) json(method, target string, body any) *httptest.ResponseRecorder {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			c.t.Fatalf("marshal: %v", err)
		}
		r = bytes.NewReader(buf)
	}
	return c.do(method, target, r)
}

// vaultID returns the single discovered vault's ID.
func (c *client) vaultID() string {
	c.t.Helper()
	rec := c.do(http.MethodGet, "/api/vaults", nil)
	if rec.Code != http.StatusOK {
		c.t.Fatalf("GET /api/vaults: %d", rec.Code)
	}
	var resp struct {
		Vaults []Vault `json:"vaults"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		c.t.Fatalf("decode: %v", err)
	}
	if len(resp.Vaults) != 1 {
		c.t.Fatalf("found %d vaults, want 1", len(resp.Vaults))
	}
	return resp.Vaults[0].ID
}

func (c *client) unlock(id string) {
	c.t.Helper()
	rec := c.json(http.MethodPost, "/api/vaults/"+id+"/unlock", unlockRequest{Password: fixturePassword})
	if rec.Code != http.StatusOK {
		c.t.Fatalf("unlock: %d %s", rec.Code, rec.Body.String())
	}
}

func TestDiscoverVaults(t *testing.T) {
	srv, _ := newTestServer(t)
	c := newClient(t, srv)

	rec := c.do(http.MethodGet, "/api/vaults", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp struct {
		Vaults []Vault `json:"vaults"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Vaults) != 1 {
		t.Fatalf("found %d vaults, want 1", len(resp.Vaults))
	}
	if resp.Vaults[0].Name != "myvault" {
		t.Errorf("name = %q, want %q", resp.Vaults[0].Name, "myvault")
	}
	if resp.Vaults[0].Unlocked {
		t.Error("vault reported unlocked before any unlock")
	}
	// The response must not disclose where the vault lives on disk.
	if strings.Contains(rec.Body.String(), string(filepath.Separator)+"myvault") {
		t.Errorf("filesystem path leaked in response: %s", rec.Body.String())
	}
}

func TestUnlockWrongPassword(t *testing.T) {
	srv, _ := newTestServer(t)
	c := newClient(t, srv)
	id := c.vaultID()

	rec := c.json(http.MethodPost, "/api/vaults/"+id+"/unlock", unlockRequest{Password: "nope"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	// And the vault must still be locked.
	if rec := c.do(http.MethodGet, "/api/vaults/"+id+"/list?path=", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("list after failed unlock: %d, want 401", rec.Code)
	}
}

func TestUnlockUnknownVault(t *testing.T) {
	srv, _ := newTestServer(t)
	c := newClient(t, srv)
	rec := c.json(http.MethodPost, "/api/vaults/deadbeef/unlock", unlockRequest{Password: fixturePassword})
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestLockedEndpointsRejected(t *testing.T) {
	srv, _ := newTestServer(t)
	c := newClient(t, srv)
	id := c.vaultID()

	for _, target := range []string{
		"/api/vaults/" + id + "/list?path=",
		"/api/vaults/" + id + "/file?path=large.bin",
		"/api/vaults/" + id + "/thumb?path=photo.jpg",
	} {
		if rec := c.do(http.MethodGet, target, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s while locked: %d, want 401", target, rec.Code)
		}
	}
}

func TestListDirectory(t *testing.T) {
	srv, _ := newTestServer(t)
	c := newClient(t, srv)
	id := c.vaultID()
	c.unlock(id)

	rec := c.do(http.MethodGet, "/api/vaults/"+id+"/list?path=", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var resp listResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Parent != nil {
		t.Error("root listing should have no parent")
	}

	byName := map[string]vault.Entry{}
	for _, e := range resp.Entries {
		byName[e.Name] = e
	}
	if e, ok := byName["photo.jpg"]; !ok || e.Media != vault.MediaImage {
		t.Errorf("photo.jpg = %+v, want an image entry", e)
	}
	if e, ok := byName["clip.mp4"]; !ok || e.Media != vault.MediaVideo || e.Size != 60000 {
		t.Errorf("clip.mp4 = %+v, want a 60000-byte video entry", e)
	}
	if _, ok := byName["gocryptfs.conf"]; ok {
		t.Error("gocryptfs.conf leaked into the listing")
	}

	// Nested listing reports its parent so the UI can render a breadcrumb.
	rec = c.do(http.MethodGet, "/api/vaults/"+id+"/list?path=subdir/deeper", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("nested list: %d", rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Parent == nil || *resp.Parent != "subdir" {
		t.Errorf("parent = %v, want %q", resp.Parent, "subdir")
	}
}

func TestServeFileFullAndRange(t *testing.T) {
	srv, _ := newTestServer(t)
	c := newClient(t, srv)
	id := c.vaultID()
	c.unlock(id)

	want := patternBytes(9, 60000)

	rec := c.do(http.MethodGet, "/api/vaults/"+id+"/file?path=clip.mp4", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), want) {
		t.Error("full file body does not match plaintext")
	}
	if got := rec.Header().Get("Content-Type"); got != "video/mp4" {
		t.Errorf("Content-Type = %q, want video/mp4", got)
	}
	if got := rec.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Errorf("Accept-Ranges = %q, want bytes", got)
	}

	// Decrypted content must never land in a browser's on-disk cache.
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q, want it to contain no-store", cc)
	}
	if lm := rec.Header().Get("Last-Modified"); lm != "" {
		t.Errorf("Last-Modified = %q, want it absent", lm)
	}

	// A range request is what makes video scrubbing work.
	req := httptest.NewRequest(http.MethodGet, "/api/vaults/"+id+"/file?path=clip.mp4", nil)
	req.AddCookie(c.cookie)
	req.Header.Set("Range", "bytes=20000-29999")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusPartialContent {
		t.Fatalf("range status = %d, want 206", rr.Code)
	}
	if got, w := rr.Header().Get("Content-Range"), "bytes 20000-29999/60000"; got != w {
		t.Errorf("Content-Range = %q, want %q", got, w)
	}
	if !bytes.Equal(rr.Body.Bytes(), want[20000:30000]) {
		t.Error("ranged body does not match plaintext at that offset")
	}
}

func TestServeFileNotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	c := newClient(t, srv)
	id := c.vaultID()
	c.unlock(id)

	if rec := c.do(http.MethodGet, "/api/vaults/"+id+"/file?path=nope.bin", nil); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestThumbnail(t *testing.T) {
	srv, _ := newTestServer(t)
	c := newClient(t, srv)
	id := c.vaultID()
	c.unlock(id)

	for _, name := range []string{"photo.jpg", "photo.png"} {
		rec := c.do(http.MethodGet, "/api/vaults/"+id+"/thumb?path="+name+"&size=64", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("thumb %s: status = %d", name, rec.Code)
		}
		if got := rec.Header().Get("Content-Type"); got != "image/jpeg" {
			t.Errorf("thumb %s: Content-Type = %q", name, got)
		}
		if !bytes.HasPrefix(rec.Body.Bytes(), []byte{0xFF, 0xD8}) {
			t.Errorf("thumb %s: body is not a JPEG", name)
		}
		if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
			t.Errorf("thumb %s: Cache-Control = %q, want no-store", name, cc)
		}
		// A second request should hit the encrypted cache and match.
		again := c.do(http.MethodGet, "/api/vaults/"+id+"/thumb?path="+name+"&size=64", nil)
		if !bytes.Equal(rec.Body.Bytes(), again.Body.Bytes()) {
			t.Errorf("thumb %s: cached response differs from the first", name)
		}
	}

	// Formats the server cannot decode return 204 so the UI shows a placeholder.
	for _, name := range []string{"clip.mp4", "large.bin"} {
		rec := c.do(http.MethodGet, "/api/vaults/"+id+"/thumb?path="+name, nil)
		if rec.Code != http.StatusNoContent {
			t.Errorf("thumb %s: status = %d, want 204", name, rec.Code)
		}
	}
}

// TestThumbnailCacheIsEncrypted checks that cached previews are never held as
// plaintext in memory: the raw cache entry must not contain the JPEG.
func TestThumbnailCacheIsEncrypted(t *testing.T) {
	srv, _ := newTestServer(t)
	c := newClient(t, srv)
	id := c.vaultID()
	c.unlock(id)

	rec := c.do(http.MethodGet, "/api/vaults/"+id+"/thumb?path=photo.jpg&size=64", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("thumb: %d", rec.Code)
	}
	plaintext := rec.Body.Bytes()

	b, ok := srv.existingSession(&http.Request{Header: http.Header{"Cookie": {c.cookie.String()}}})
	if !ok {
		t.Fatal("session missing")
	}
	u, ok := b.unlocked(id)
	if !ok {
		t.Fatal("vault not unlocked")
	}

	u.thumbs.mu.Lock()
	var sealedFound bool
	for _, el := range u.thumbs.entries {
		sealed := el.Value.(*blobEntry).sealed
		sealedFound = true
		if bytes.Contains(sealed, plaintext) {
			t.Error("cache entry holds the thumbnail in plaintext")
		}
		if bytes.HasPrefix(sealed, []byte{0xFF, 0xD8}) {
			t.Error("cache entry starts with a JPEG marker; it is not encrypted")
		}
	}
	u.thumbs.mu.Unlock()

	if !sealedFound {
		t.Fatal("nothing was cached, so the encryption check proved nothing")
	}
}

// TestLockWipesCache proves locking a vault makes cached previews unreadable
// immediately rather than leaving them to expire.
func TestLockWipesCache(t *testing.T) {
	srv, _ := newTestServer(t)
	c := newClient(t, srv)
	id := c.vaultID()
	c.unlock(id)

	if rec := c.do(http.MethodGet, "/api/vaults/"+id+"/thumb?path=photo.jpg&size=64", nil); rec.Code != http.StatusOK {
		t.Fatalf("thumb: %d", rec.Code)
	}

	b, _ := srv.existingSession(&http.Request{Header: http.Header{"Cookie": {c.cookie.String()}}})
	u, _ := b.unlocked(id)
	cache := u.thumbs

	if rec := c.json(http.MethodPost, "/api/vaults/"+id+"/lock", nil); rec.Code != http.StatusOK {
		t.Fatalf("lock: %d", rec.Code)
	}

	if _, hit := cache.Get("anything"); hit {
		t.Error("cache still returns entries after lock")
	}
	if cache.crypt.aead != nil {
		t.Error("cache key was not wiped on lock")
	}
	if rec := c.do(http.MethodGet, "/api/vaults/"+id+"/list?path=", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("list after lock: %d, want 401", rec.Code)
	}
}

func TestBlobCacheRoundTripAndEviction(t *testing.T) {
	c, err := newBlobCache(1024)
	if err != nil {
		t.Fatalf("newBlobCache: %v", err)
	}
	payload := []byte("some sensitive preview bytes")
	c.Put("a", payload)

	got, ok := c.Get("a")
	if !ok || !bytes.Equal(got, payload) {
		t.Fatalf("Get(a) = %q, %v; want the original payload", got, ok)
	}

	// Filling past the budget must evict, not grow without bound.
	for i := 0; i < 200; i++ {
		c.Put(string(rune('a'+i%26))+string(rune('0'+i/26)), bytes.Repeat([]byte("x"), 100))
	}
	if c.cost > c.maxCost {
		t.Errorf("cache cost %d exceeds budget %d", c.cost, c.maxCost)
	}

	// An oversized entry is skipped rather than flushing everything.
	c.Put("huge", bytes.Repeat([]byte("y"), 5000))
	if _, ok := c.Get("huge"); ok {
		t.Error("oversized entry was cached")
	}

	c.Close()
	if _, ok := c.Get("a"); ok {
		t.Error("cache returned an entry after Close")
	}
}

func TestUploadRoundTrip(t *testing.T) {
	srv, _ := newTestServer(t)
	c := newClient(t, srv)
	id := c.vaultID()
	c.unlock(id)

	payload := patternBytes(4242, 70000)
	rec := c.do(http.MethodPost,
		"/api/vaults/"+id+"/upload?path=subdir&name=uploaded.bin",
		bytes.NewReader(payload))
	if rec.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
	}

	back := c.do(http.MethodGet, "/api/vaults/"+id+"/file?path=subdir/uploaded.bin", nil)
	if back.Code != http.StatusOK {
		t.Fatalf("read back: %d", back.Code)
	}
	if !bytes.Equal(back.Body.Bytes(), payload) {
		t.Error("uploaded content did not round-trip")
	}

	// Uploading over an existing name must not silently overwrite.
	dup := c.do(http.MethodPost,
		"/api/vaults/"+id+"/upload?path=subdir&name=uploaded.bin",
		bytes.NewReader(payload))
	if dup.Code != http.StatusConflict {
		t.Errorf("duplicate upload: %d, want 409", dup.Code)
	}
}

func TestUploadRejectsPathInName(t *testing.T) {
	srv, _ := newTestServer(t)
	c := newClient(t, srv)
	id := c.vaultID()
	c.unlock(id)

	for _, name := range []string{"../escape.bin", "sub/dir.bin", "..", "a\\b.bin"} {
		rec := c.do(http.MethodPost,
			"/api/vaults/"+id+"/upload?path=&name="+name,
			strings.NewReader("x"))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("upload name=%q: %d, want 400", name, rec.Code)
		}
	}
}

func TestMkdirMoveDelete(t *testing.T) {
	srv, _ := newTestServer(t)
	c := newClient(t, srv)
	id := c.vaultID()
	c.unlock(id)

	if rec := c.json(http.MethodPost, "/api/vaults/"+id+"/mkdir", mkdirRequest{Path: "newdir"}); rec.Code != http.StatusOK {
		t.Fatalf("mkdir: %d %s", rec.Code, rec.Body.String())
	}
	if rec := c.json(http.MethodPost, "/api/vaults/"+id+"/mkdir", mkdirRequest{Path: "newdir"}); rec.Code != http.StatusConflict {
		t.Errorf("duplicate mkdir: %d, want 409", rec.Code)
	}

	if rec := c.json(http.MethodPost, "/api/vaults/"+id+"/move",
		moveRequest{From: "clip.mp4", To: "newdir/renamed.mp4"}); rec.Code != http.StatusOK {
		t.Fatalf("move: %d %s", rec.Code, rec.Body.String())
	}
	back := c.do(http.MethodGet, "/api/vaults/"+id+"/file?path=newdir/renamed.mp4", nil)
	if back.Code != http.StatusOK || !bytes.Equal(back.Body.Bytes(), patternBytes(9, 60000)) {
		t.Error("moved file did not survive the move intact")
	}

	if rec := c.do(http.MethodDelete, "/api/vaults/"+id+"/entry?path=newdir/renamed.mp4", nil); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec := c.do(http.MethodGet, "/api/vaults/"+id+"/file?path=newdir/renamed.mp4", nil); rec.Code != http.StatusNotFound {
		t.Errorf("deleted file still served: %d", rec.Code)
	}

	// Deleting the vault root must be refused.
	if rec := c.do(http.MethodDelete, "/api/vaults/"+id+"/entry?path=", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("delete root: %d, want 400", rec.Code)
	}
}

// TestPathTraversalRejected is the HTTP-level guard: a traversal attempt in
// any path parameter must fail rather than reach outside the vault.
func TestPathTraversalRejected(t *testing.T) {
	srv, root := newTestServer(t)
	c := newClient(t, srv)
	id := c.vaultID()
	c.unlock(id)

	// A file the attacker would love to read, outside the vault.
	secret := filepath.Join(root, "secret.txt")
	if err := os.WriteFile(secret, []byte("TOP SECRET"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}

	attempts := []string{
		"../secret.txt",
		"../../etc/passwd",
		"subdir/../../secret.txt",
		"%2e%2e%2fsecret.txt",
		"..%2Fsecret.txt",
	}
	for _, p := range attempts {
		for _, endpoint := range []string{"file", "list", "thumb"} {
			rec := c.do(http.MethodGet, "/api/vaults/"+id+"/"+endpoint+"?path="+p, nil)
			if rec.Code == http.StatusOK && bytes.Contains(rec.Body.Bytes(), []byte("TOP SECRET")) {
				t.Fatalf("%s?path=%s escaped the vault", endpoint, p)
			}
			if rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound && rec.Code != http.StatusNoContent {
				t.Errorf("%s?path=%s: status %d, want 400/404/204", endpoint, p, rec.Code)
			}
		}
	}
}

// TestCrossOriginRejected checks the CSRF guard on mutating endpoints.
func TestCrossOriginRejected(t *testing.T) {
	srv, _ := newTestServer(t)
	c := newClient(t, srv)
	id := c.vaultID()
	c.unlock(id)

	body, _ := json.Marshal(mkdirRequest{Path: "evil"})
	req := httptest.NewRequest(http.MethodPost, "/api/vaults/"+id+"/mkdir", bytes.NewReader(body))
	req.AddCookie(c.cookie)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-origin mkdir: %d, want 403", rec.Code)
	}

	// Same-origin must still work.
	req = httptest.NewRequest(http.MethodPost, "/api/vaults/"+id+"/mkdir", bytes.NewReader(body))
	req.AddCookie(c.cookie)
	req.Header.Set("Origin", "http://"+req.Host)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("same-origin mkdir: %d, want 200", rec.Code)
	}
}

// TestSessionsAreIsolated makes sure one browser unlocking a vault does not
// unlock it for everyone.
func TestSessionsAreIsolated(t *testing.T) {
	srv, _ := newTestServer(t)
	alice := newClient(t, srv)
	id := alice.vaultID()
	alice.unlock(id)

	bob := newClient(t, srv)
	if rec := bob.do(http.MethodGet, "/api/vaults/"+id+"/list?path=", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("second session sees an unlocked vault: %d, want 401", rec.Code)
	}
	if rec := alice.do(http.MethodGet, "/api/vaults/"+id+"/list?path=", nil); rec.Code != http.StatusOK {
		t.Errorf("first session lost access: %d", rec.Code)
	}
}

func TestLogoutClosesEverything(t *testing.T) {
	srv, _ := newTestServer(t)
	c := newClient(t, srv)
	id := c.vaultID()
	c.unlock(id)

	if rec := c.json(http.MethodPost, "/api/logout", nil); rec.Code != http.StatusOK {
		t.Fatalf("logout: %d", rec.Code)
	}
	if rec := c.do(http.MethodGet, "/api/vaults/"+id+"/list?path=", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("access after logout: %d, want 401", rec.Code)
	}
}

func TestIdleSessionsExpire(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.sessions.idleTimeout = 20 * time.Millisecond

	c := newClient(t, srv)
	id := c.vaultID()
	c.unlock(id)

	time.Sleep(60 * time.Millisecond)

	if rec := c.do(http.MethodGet, "/api/vaults/"+id+"/list?path=", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("idle session still unlocked: %d, want 401", rec.Code)
	}
}

func TestUnlockRateLimited(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.limiter = newRateLimiter(3, time.Minute)
	c := newClient(t, srv)
	id := c.vaultID()

	var limited bool
	for i := 0; i < 6; i++ {
		rec := c.json(http.MethodPost, "/api/vaults/"+id+"/unlock", unlockRequest{Password: "wrong"})
		if rec.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Error("repeated wrong passwords were never rate limited")
	}
}

func TestUnknownAPIEndpoint404(t *testing.T) {
	srv, _ := newTestServer(t)
	c := newClient(t, srv)
	rec := c.do(http.MethodGet, "/api/nope", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "<html") {
		t.Error("unknown API path fell through to the SPA")
	}
}

func TestSPAFallback(t *testing.T) {
	srv, _ := newTestServer(t)
	c := newClient(t, srv)
	rec := c.do(http.MethodGet, "/some/client/route", nil)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (SPA fallback)", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
}

package drive_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/adrianm/imagethat/core/cache"
	"github.com/adrianm/imagethat/core/drive"
	"github.com/adrianm/imagethat/core/drive/drivetest"
	"github.com/adrianm/imagethat/core/vault"
	"github.com/adrianm/imagethat/core/xfer"
)

const pw = "correct horse"

func randBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

// newVault makes an empty vault in a fresh Drive folder and opens it.
func newVault(t *testing.T, srv *drivetest.Server, ranges *cache.Ranges) (*vault.Session, *drive.VaultFS, string) {
	t.Helper()
	root := srv.AddFolder("root", "Vault")
	if err := drive.InitVault(srv.Client(), root, pw); err != nil {
		t.Fatalf("InitVault: %v", err)
	}
	s, v, err := drive.OpenVault(srv.Client(), root, pw, ranges)
	if err != nil {
		t.Fatalf("OpenVault: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, v, root
}

func names(t *testing.T, s *vault.Session, dir string) []string {
	t.Helper()
	entries, err := s.ListDir(dir)
	if err != nil {
		t.Fatalf("ListDir(%q): %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name)
	}
	sort.Strings(out)
	return out
}

func TestVaultWriteReadAndLayout(t *testing.T) {
	srv := drivetest.New()
	defer srv.Close()
	s, _, root := newVault(t, srv, nil)

	small := []byte("hello drive")
	if _, err := s.Create("a.txt", bytes.NewReader(small)); err != nil {
		t.Fatal(err)
	}
	if err := s.Mkdir("Trip"); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("long name ", 20) + ".jpg"
	big := randBytes(t, 300_000)
	if _, err := s.Create("Trip/"+long, bytes.NewReader(big)); err != nil {
		t.Fatal(err)
	}
	if got := names(t, s, ""); strings.Join(got, ",") != "Trip,a.txt" {
		t.Fatalf("root = %v", got)
	}
	if got, err := s.ReadFile("Trip/" + long); err != nil || !bytes.Equal(got, big) {
		t.Fatalf("read long: %v", err)
	}

	// Plaintext names never reach Drive.
	for _, n := range srv.Children(root) {
		if strings.Contains(n, "Trip") || strings.Contains(n, "a.txt") {
			t.Fatalf("plaintext name on Drive: %q", n)
		}
	}

	// Rename and move, replace, delete.
	if err := s.Move("a.txt", "Trip/b.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Replace("Trip/b.txt", strings.NewReader("replaced")); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ReadFile("Trip/b.txt"); string(got) != "replaced" {
		t.Fatalf("after replace: %q", got)
	}
	if err := s.Remove("Trip/" + long); err != nil {
		t.Fatal(err)
	}
	if got := names(t, s, "Trip"); strings.Join(got, ",") != "b.txt" {
		t.Fatalf("Trip = %v", got)
	}

	// The same files read by a fresh session (no index, all from Drive) and,
	// laid out on disk, by the local engine: the Drive layout is gocryptfs'.
	s2, _, err := drive.OpenVault(srv.Client(), root, pw, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if got, _ := s2.ReadFile("Trip/b.txt"); string(got) != "replaced" {
		t.Fatalf("fresh session: %q", got)
	}
	dir := t.TempDir()
	export(t, srv, root, dir)
	local, err := vault.OpenLocal(dir, pw)
	if err != nil {
		t.Fatalf("OpenLocal of exported Drive vault: %v", err)
	}
	defer local.Close()
	if got, err := local.ReadFile("Trip/b.txt"); err != nil || string(got) != "replaced" {
		t.Fatalf("local read: %q %v", got, err)
	}
}

// export copies a Drive folder tree to dir.
func export(t *testing.T, srv *drivetest.Server, folder, dir string) {
	t.Helper()
	c := srv.Client()
	files, err := c.List(context.Background(), folder)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		p := filepath.Join(dir, f.Name)
		if f.IsFolder {
			os.MkdirAll(p, 0o700)
			export(t, srv, f.ID, p)
			continue
		}
		if err := os.WriteFile(p, srv.Content(f.ID), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOpenExistingVaultFromDisk(t *testing.T) {
	// A vault made by the local engine and uploaded as is (as Drive for
	// desktop would) opens through Drive.
	dir := t.TempDir()
	if err := vault.Init(vault.NewLocalFS(dir), pw); err != nil {
		t.Fatal(err)
	}
	local, err := vault.OpenLocal(dir, pw)
	if err != nil {
		t.Fatal(err)
	}
	local.Mkdir("Pics")
	local.Create("Pics/x.jpg", strings.NewReader("jpeg bytes"))
	local.Close()

	srv := drivetest.New()
	defer srv.Close()
	srv.PageSize = 2 // listings must follow pages
	root := srv.AddFolder("root", "Vault")
	upload(t, srv, dir, root)

	s, _, err := drive.OpenVault(srv.Client(), root, pw, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got, err := s.ReadFile("Pics/x.jpg"); err != nil || string(got) != "jpeg bytes" {
		t.Fatalf("read: %q %v", got, err)
	}
	if _, _, err := drive.OpenVault(srv.Client(), root, "wrong", nil); !errors.Is(err, vault.ErrWrongPassword) {
		t.Fatalf("wrong password: %v", err)
	}
}

func upload(t *testing.T, srv *drivetest.Server, dir, folder string) {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if e.IsDir() {
			upload(t, srv, p, srv.AddFolder(folder, e.Name()))
			continue
		}
		data, _ := os.ReadFile(p)
		srv.AddFile(folder, e.Name(), "application/octet-stream", data)
	}
}

func TestReadsComeFromCacheAndWindows(t *testing.T) {
	defer drive.SetReadSizes(200_000, 64<<10)()
	srv := drivetest.New()
	defer srv.Close()
	ranges, err := cache.NewRanges(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	s, _, _ := newVault(t, srv, ranges)
	video := randBytes(t, 1_000_000) // over wholeFileMax: read in windows
	if _, err := s.Create("v.mp4", bytes.NewReader(video)); err != nil {
		t.Fatal(err)
	}

	f, err := s.Open("v.mp4")
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4096)
	before := srv.MediaRequests.Load()
	if _, err := f.ReadAt(buf, 0); err != nil {
		t.Fatal(err)
	}
	afterFirst := srv.MediaRequests.Load()
	// The rest of the 64 KiB window is already here.
	for off := int64(4096); off < 40_000; off += 4096 {
		if _, err := f.ReadAt(buf, off); err != nil {
			t.Fatal(err)
		}
	}
	if srv.MediaRequests.Load() != afterFirst || afterFirst-before > 2 {
		t.Fatalf("media requests: before %d, first read %d, now %d", before, afterFirst, srv.MediaRequests.Load())
	}
	if !bytes.Equal(buf, video[36864:36864+4096]) {
		t.Fatal("wrong bytes")
	}
	f.Close()

	// A second session over the same cache needs no downloads for that part.
	all, err := s.ReadFile("v.mp4")
	if err != nil || !bytes.Equal(all, video) {
		t.Fatalf("full read: %v", err)
	}
	n := srv.MediaRequests.Load()
	if again, _ := s.ReadFile("v.mp4"); !bytes.Equal(again, video) || srv.MediaRequests.Load() != n {
		t.Fatalf("second full read went to Drive: %d -> %d", n, srv.MediaRequests.Load())
	}
}

func TestCacheFilePinsAndSurvivesFlakyDownloads(t *testing.T) {
	defer drive.SetReadSizes(100_000, 32<<10)()
	srv := drivetest.New()
	defer srv.Close()
	ranges, err := cache.NewRanges(t.TempDir(), 1) // tiny budget: unpinned data is evicted
	if err != nil {
		t.Fatal(err)
	}
	s, v, _ := newVault(t, srv, ranges)
	data := randBytes(t, 400_000)
	s.Create("clip.mov", bytes.NewReader(data))

	if s.IsCached("clip.mov") {
		t.Fatal("cached before CacheFile")
	}
	srv.FailMedia(2)
	var p xfer.Progress
	if err := s.CacheFile(context.Background(), "clip.mov", xfer.Options{BaseDelay: time.Millisecond, Progress: &p}); err != nil {
		t.Fatalf("CacheFile: %v", err)
	}
	if !s.IsCached("clip.mov") {
		t.Fatal("not cached after CacheFile")
	}
	if _, _, retries := p.Snapshot(); retries == 0 {
		t.Fatal("expected retries")
	}
	cp, _ := s.CipherPath("clip.mov")
	if fr := v.CachedFractions(cp); len(fr) != 1 || fr[0][0] != 0 || fr[0][1] != 1 {
		t.Fatalf("fractions = %v", fr)
	}

	// Offline: every download fails, the file still reads.
	srv.FailMedia(1 << 30)
	if got, err := s.ReadFile("clip.mov"); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("offline read: %v", err)
	}
	srv.FailMedia(0)

	if err := s.Uncache("clip.mov"); err != nil {
		t.Fatal(err)
	}
	if s.IsCached("clip.mov") {
		t.Fatal("still cached after Uncache")
	}
}

func TestChangedElsewhereIsRefetched(t *testing.T) {
	srv := drivetest.New()
	defer srv.Close()
	ranges, _ := cache.NewRanges(t.TempDir(), 0)
	s, v, root := newVault(t, srv, ranges)
	s.Create("n.txt", strings.NewReader("one"))
	if got, _ := s.ReadFile("n.txt"); string(got) != "one" {
		t.Fatal(string(got))
	}

	// Another device rewrites the file: its ciphertext comes from a second
	// session's write, and our index learns of it on Refresh.
	other, _, err := drive.OpenVault(srv.Client(), root, pw, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Replace("n.txt", strings.NewReader("two!")); err != nil {
		t.Fatal(err)
	}
	other.Close()
	v.Refresh("")
	if got, err := s.ReadFile("n.txt"); err != nil || string(got) != "two!" {
		t.Fatalf("after change elsewhere: %q %v", got, err)
	}
}

func TestLargeUploadIsResumable(t *testing.T) {
	defer drive.SetUploadChunk(256 << 10)()
	srv := drivetest.New()
	defer srv.Close()
	s, _, _ := newVault(t, srv, nil)
	data := randBytes(t, 1_000_000)
	if _, err := s.Create("big.bin", bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ReadFile("big.bin"); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("read back: %v", err)
	}
}

func TestTokenRefreshAndRevocation(t *testing.T) {
	srv := drivetest.New()
	defer srv.Close()
	c := srv.Client()
	if _, err := c.List(context.Background(), "root"); err != nil {
		t.Fatal(err)
	}
	srv.ExpireToken()
	if _, err := c.List(context.Background(), "root"); err != nil {
		t.Fatalf("after expiry: %v", err)
	}

	cfg := srv.Config()
	cfg.RefreshToken = "revoked"
	if _, err := drive.New(cfg).List(context.Background(), "root"); !errors.Is(err, drive.ErrAuth) {
		t.Fatalf("revoked: %v", err)
	}
	cfg.RefreshToken = ""
	if _, err := drive.New(cfg).List(context.Background(), "root"); !errors.Is(err, drive.ErrNotConnected) {
		t.Fatalf("no token: %v", err)
	}
	if _, err := c.Download(context.Background(), "nope"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing file: %v", err)
	}
}

func TestSignInFlow(t *testing.T) {
	srv := drivetest.New()
	defer srv.Close()
	cfg := srv.Config()
	cfg.RefreshToken = ""
	c := drive.New(cfg)
	flow, err := c.StartSignIn()
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(flow.URL)
	q := u.Query()
	if q.Get("code_challenge") == "" || q.Get("scope") != drive.Scope {
		t.Fatalf("auth URL: %s", flow.URL)
	}

	// The browser comes back: once with a forged state (ignored), then for real.
	redirect := q.Get("redirect_uri")
	if resp, err := http.Get(redirect + "?state=forged&code=good-code"); err == nil {
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("forged state: %d", resp.StatusCode)
		}
	}
	go func() {
		resp, err := http.Get(redirect + "?state=" + url.QueryEscape(q.Get("state")) + "&code=good-code")
		if err == nil {
			resp.Body.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := flow.Wait(ctx); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if c.RefreshToken() == "" {
		t.Fatal("no refresh token")
	}
	if _, err := c.List(context.Background(), "root"); err != nil {
		t.Fatal(err)
	}

	flow2, _ := c.StartSignIn()
	flow2.Cancel()
	if err := flow2.Wait(context.Background()); !errors.Is(err, drive.ErrCancelled) {
		t.Fatalf("cancelled: %v", err)
	}
}

func TestFindVaultRootAndPlainBrowsing(t *testing.T) {
	srv := drivetest.New()
	defer srv.Close()
	root := srv.AddFolder("root", "Vault")
	if err := drive.InitVault(srv.Client(), root, pw); err != nil {
		t.Fatal(err)
	}
	inner := srv.AddFolder(root, "cipherdir")
	deeper := srv.AddFolder(inner, "cipherdir2")
	c := srv.Client()
	ctx := context.Background()

	got, err := c.FindVaultRoot(ctx, deeper, 0)
	if err != nil || got.RootID != root || strings.Join(got.Path, "/") != "cipherdir/cipherdir2" {
		t.Fatalf("FindVaultRoot = %+v, %v", got, err)
	}
	if got, err := c.FindVaultRoot(ctx, root, 0); err != nil || got.RootID != root || len(got.Path) != 0 {
		t.Fatalf("root itself = %+v, %v", got, err)
	}
	plain := srv.AddFolder("root", "Photos")
	if _, err := c.FindVaultRoot(ctx, plain, 0); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("plain folder: %v", err)
	}

	srv.AddFile(plain, "a.jpg", "image/jpeg", []byte("A"))
	srv.AddFile(plain, "notes.txt", "text/plain", []byte("N"))
	srv.AddFolder(plain, "Sub")
	media, err := c.ListMedia(ctx, plain)
	if err != nil || len(media) != 2 {
		t.Fatalf("ListMedia = %+v, %v", media, err)
	}
	var img drive.File
	for _, m := range media {
		if m.Name == "a.jpg" {
			img = m
		}
	}
	if th, err := c.Thumbnail(ctx, img.ThumbnailLink, 400); err != nil || !strings.HasSuffix(string(th), "=s400") {
		t.Fatalf("thumbnail = %q, %v", th, err)
	}
}

package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/adrianm/imagethat/core/cache"
	"github.com/adrianm/imagethat/core/vault"
	"github.com/adrianm/imagethat/core/xfer"
)

const testPassword = "cache-test-password"

// fakeServer serves a vault directory over the raw endpoints the way
// imagethat-web does. failEvery makes every n-th file request fail with a 503;
// down makes all of them fail.
type fakeServer struct {
	dir       string
	failEvery atomic.Int64
	down      atomic.Bool
	fileReqs  atomic.Int64
	allReqs   atomic.Int64
}

func (f *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.allReqs.Add(1)
	switch {
	case r.URL.Path == "/api/login":
		w.WriteHeader(http.StatusOK)
	case strings.HasSuffix(r.URL.Path, "/raw/list"):
		entries, err := os.ReadDir(filepath.Join(f.dir, r.URL.Query().Get("path")))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		type entry struct {
			Name  string `json:"name"`
			IsDir bool   `json:"isDir"`
			Size  int64  `json:"size"`
		}
		var out []entry
		for _, e := range entries {
			info, _ := e.Info()
			out = append(out, entry{e.Name(), e.IsDir(), info.Size()})
		}
		json.NewEncoder(w).Encode(map[string]any{"entries": out})
	case strings.HasSuffix(r.URL.Path, "/raw/file"):
		n := f.fileReqs.Add(1)
		if f.down.Load() || (f.failEvery.Load() > 0 && n%f.failEvery.Load() == 0) {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		file, err := os.Open(filepath.Join(f.dir, r.URL.Query().Get("path")))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer file.Close()
		http.ServeContent(w, r, "", time.Time{}, file)
	default:
		http.NotFound(w, r)
	}
}

// setup makes a vault with one file, serves it, and opens it remotely through
// a read-through cache in cacheDir. local is a session on the server's copy,
// for changing it.
func setup(t *testing.T, content []byte) (remote, local *vault.Session, fake *fakeServer, cacheDir string) {
	t.Helper()
	dir := t.TempDir()
	if err := vault.Init(vault.NewLocalFS(dir), testPassword); err != nil {
		t.Fatal(err)
	}
	local, err := vault.OpenLocal(dir, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { local.Close() })
	if _, err := local.Create("clip.mov", bytes.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	fake = &fakeServer{dir: dir}
	ts := httptest.NewServer(fake)
	t.Cleanup(ts.Close)
	c, err := New(ts.URL, Options{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Login(context.Background(), "access"); err != nil {
		t.Fatal(err)
	}
	cacheDir = t.TempDir()
	ranges, err := cache.NewRanges(cacheDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	remote, err = c.OpenVaultCached(context.Background(), "v1", testPassword, ranges)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { remote.Close() })
	return remote, local, fake, cacheDir
}

func pattern(seed byte, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7) ^ seed
	}
	return b
}

func quick() xfer.Options {
	return xfer.Options{ChunkSize: 16 << 10, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}
}

func TestCacheSurvivesFlakyServerAndServesOffline(t *testing.T) {
	want := pattern(1, 300_000)
	remote, _, fake, cacheDir := setup(t, want)

	fake.failEvery.Store(3)
	var p xfer.Progress
	opts := quick()
	opts.Progress = &p
	if err := remote.CacheFile(context.Background(), "clip.mov", opts); err != nil {
		t.Fatal(err)
	}
	if !remote.IsCached("clip.mov") {
		t.Fatal("IsCached false after CacheFile")
	}
	if _, _, retries := p.Snapshot(); retries == 0 {
		t.Fatal("the flaky server should have caused retries")
	}
	fake.failEvery.Store(0)

	// Caching it again only checks it: one small request, no download.
	before := fake.fileReqs.Load()
	if err := remote.CacheFile(context.Background(), "clip.mov", quick()); err != nil {
		t.Fatal(err)
	}
	if n := fake.fileReqs.Load() - before; n != 1 {
		t.Fatalf("re-caching made %d file requests, want 1", n)
	}

	// The cache holds ciphertext only: the plaintext is nowhere in it.
	entries, _ := os.ReadDir(cacheDir)
	for _, e := range entries {
		data, _ := os.ReadFile(filepath.Join(cacheDir, e.Name()))
		if bytes.Contains(data, want[:4096]) {
			t.Fatal("plaintext found in the cache directory")
		}
	}

	fake.down.Store(true)
	before = fake.fileReqs.Load()
	got, err := remote.ReadFile("clip.mov")
	if err != nil {
		t.Fatalf("offline read of a cached file: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("offline read differs")
	}
	// Only the validation request should have gone to the server.
	if n := fake.fileReqs.Load() - before; n > 1 {
		t.Fatalf("%d file requests while reading a cached file, want at most 1", n)
	}
}

func TestStaleCacheIsDropped(t *testing.T) {
	remote, local, _, _ := setup(t, pattern(1, 50_000))
	if err := remote.CacheFile(context.Background(), "clip.mov", quick()); err != nil {
		t.Fatal(err)
	}
	// Same size, new content: only the header ID tells them apart.
	newer := pattern(2, 50_000)
	if _, err := local.Replace("clip.mov", bytes.NewReader(newer)); err != nil {
		t.Fatal(err)
	}
	got, err := remote.ReadFile("clip.mov")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, newer) {
		t.Fatal("read the stale cached copy")
	}
	// Ranges drops an entry, pin included, once the version differs; reading
	// the new version caches it afresh.
}

func TestReadsAreCached(t *testing.T) {
	want := pattern(3, 200_000)
	remote, _, fake, _ := setup(t, want)
	if _, err := remote.ReadFile("clip.mov"); err != nil {
		t.Fatal(err)
	}
	// Watching it again reads from disk: only the version check goes out.
	before := fake.fileReqs.Load()
	got, err := remote.ReadFile("clip.mov")
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("second read: %v", err)
	}
	if n := fake.fileReqs.Load() - before; n > 1 {
		t.Fatalf("second read made %d file requests, want at most 1", n)
	}
	if fr := remote.CachedFractions("clip.mov"); len(fr) != 1 || fr[0] != [2]float64{0, 1} {
		t.Fatalf("CachedFractions = %v, want [[0 1]]", fr)
	}
	if !remote.IsCached("clip.mov") {
		t.Fatal("a file read whole should report as held")
	}
}

func TestPrefetchFillsTheCache(t *testing.T) {
	restore := cache.WholeFileMax
	cache.WholeFileMax, cache.Window = 64<<10, 64<<10
	t.Cleanup(func() { cache.WholeFileMax, cache.Window = restore, 8<<20 })
	remote, _, _, _ := setup(t, pattern(4, 1_000_000))
	if err := remote.Prefetch(context.Background(), "clip.mov", 200_000, 50_000); err != nil {
		t.Fatal(err)
	}
	fr := remote.CachedFractions("clip.mov")
	if len(fr) != 2 || fr[0][0] != 0 || fr[1][1] != 1 {
		t.Fatalf("CachedFractions after Prefetch = %v, want the head and the tail", fr)
	}
}

func TestDeletedOnServerDropsCache(t *testing.T) {
	remote, local, _, _ := setup(t, pattern(1, 10_000))
	if err := remote.CacheFile(context.Background(), "clip.mov", quick()); err != nil {
		t.Fatal(err)
	}
	if err := local.Remove("clip.mov"); err != nil {
		t.Fatal(err)
	}
	if _, err := remote.ReadFile("clip.mov"); err == nil {
		t.Fatal("read a file deleted on the server")
	}
	if remote.IsCached("clip.mov") {
		t.Fatal("copy of a deleted file kept")
	}
}

func TestUncache(t *testing.T) {
	remote, _, _, cacheDir := setup(t, pattern(1, 10_000))
	if err := remote.CacheFile(context.Background(), "clip.mov", quick()); err != nil {
		t.Fatal(err)
	}
	if err := remote.Uncache("clip.mov"); err != nil {
		t.Fatal(err)
	}
	if remote.IsCached("clip.mov") {
		t.Fatal("still cached")
	}
	if fr := remote.CachedFractions("clip.mov"); len(fr) != 0 {
		t.Fatalf("still holds %v after Uncache", fr)
	}
	_ = cacheDir
}

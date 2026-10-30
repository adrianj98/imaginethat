package mobile

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adrianm/imagethat/core/drive/drivetest"
)

func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 64, 48))
	for x := 0; x < 64; x++ {
		img.Set(x, x%48, color.RGBA{200, 10, 10, 255})
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// A Drive vault through the binding: create, write, read, thumbnail, cache,
// and a fault-tolerant copy into the offline vault.
func TestDriveVault(t *testing.T) {
	srv := drivetest.New()
	defer srv.Close()
	d := newDriveWith(srv.Config())
	if !d.Connected() {
		t.Fatal("not connected")
	}
	folder := srv.AddFolder("root", "Phone vault")
	if err := InitDrive(d, folder, "pw"); err != nil {
		t.Fatal(err)
	}
	cacheDir := t.TempDir()
	v, err := OpenDrive(d, folder, "pw", cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	if _, err := OpenDrive(d, folder, "nope", ""); err == nil || !strings.HasPrefix(err.Error(), "wrong_password:") {
		t.Fatalf("wrong password: %v", err)
	}

	pic := pngBytes(t)
	if err := v.Mkdir("Pics"); err != nil {
		t.Fatal(err)
	}
	put(t, v, "Pics/a.png", pic)
	if got := names(t, v, "Pics"); len(got) != 1 || got[0] != "a.png" {
		t.Fatalf("names = %v", got)
	}
	if got, err := v.ReadRange("Pics/a.png", 0, len(pic)); err != nil || !bytes.Equal(got, pic) {
		t.Fatalf("read: %v", err)
	}

	if err := v.ConfigureThumbnails("drive:"+folder, t.TempDir(), 0, 0); err != nil {
		t.Fatal(err)
	}
	if th, err := v.Thumbnail("Pics/a.png", 32); err != nil || len(th) < 2 || th[0] != 0xFF || th[1] != 0xD8 {
		t.Fatalf("thumbnail: %d bytes, %v", len(th), err)
	}

	job := NewJob()
	if err := v.CacheFile(job, "Pics/a.png"); err != nil {
		t.Fatal(err)
	}
	if !v.IsCached("Pics/a.png") || DriveCacheSize(cacheDir) == 0 {
		t.Fatal("not cached")
	}
	var fr [][2]float64
	js, _ := v.CachedRanges("Pics/a.png")
	if json.Unmarshal([]byte(js), &fr); len(fr) != 1 || fr[0][1] != 1 {
		t.Fatalf("CachedRanges = %s", js)
	}

	// Copy into a local vault through a flaky Drive.
	local, _ := newVault(t)
	v.Uncache("Pics/a.png")
	ClearDriveCache(cacheDir)
	srv.FailMedia(1)
	copyJob := NewJob()
	if err := local.CopyFrom(copyJob, "a.png", v, "Pics/a.png"); err != nil {
		t.Fatalf("CopyFrom: %v", err)
	}
	if copyJob.Retries() == 0 || copyJob.Done() != int64(len(pic)) {
		t.Fatalf("job: done %d retries %d", copyJob.Done(), copyJob.Retries())
	}
	if got, _ := local.ReadRange("a.png", 0, len(pic)); !bytes.Equal(got, pic) {
		t.Fatal("copy differs")
	}

	// A cancelled copy fails with "cancelled:" and leaves nothing.
	cancelled := NewJob()
	cancelled.Cancel()
	if err := local.CopyFrom(cancelled, "b.png", v, "Pics/a.png"); err == nil || !strings.HasPrefix(err.Error(), "cancelled:") {
		t.Fatalf("cancelled copy: %v", err)
	}
	if got := names(t, local, ""); len(got) != 1 {
		t.Fatalf("after cancel: %v", got)
	}

	// Plain browsing.
	js, err = d.ListMedia(folder)
	if err != nil || js == "" {
		t.Fatal(err)
	}
	if root, err := d.FindVaultRoot(folder); err != nil || !strings.Contains(root, folder) {
		t.Fatalf("FindVaultRoot = %q, %v", root, err)
	}
	if root, err := d.FindVaultRoot("root"); err != nil || root != "" {
		t.Fatalf("My Drive is no vault: %q, %v", root, err)
	}
}

func TestCopyFileAndThumbnailDiskCache(t *testing.T) {
	v, _ := newVault(t)
	src := filepath.Join(t.TempDir(), "in.png")
	pic := pngBytes(t)
	os.WriteFile(src, pic, 0o600)
	if err := v.CopyFile(NewJob(), "in.png", src); err != nil {
		t.Fatal(err)
	}
	disk := t.TempDir()
	v.ConfigureThumbnails("local:test", disk, 0, 0)
	th, err := v.Thumbnail("in.png", 32)
	if err != nil || len(th) == 0 {
		t.Fatalf("thumbnail: %v", err)
	}
	if ThumbCacheSize(disk) == 0 {
		t.Fatal("thumbnail not on disk")
	}
	// What is on disk is sealed: no JPEG in the clear.
	filepath.Walk(disk, func(p string, info os.FileInfo, err error) error {
		if info != nil && !info.IsDir() {
			b, _ := os.ReadFile(p)
			if bytes.Contains(b, th[:4]) {
				t.Errorf("plaintext JPEG in %s", p)
			}
		}
		return nil
	})
	// Formats Go cannot decode come back empty, and the device's own render
	// is kept.
	put(t, v, "x.heic", []byte("not decodable"))
	if th, err := v.Thumbnail("x.heic", 32); err != nil || len(th) != 0 {
		t.Fatalf("heic: %d bytes, %v", len(th), err)
	}
	v.StoreThumbnail("x.heic", 32, th)
	if got, _ := v.Thumbnail("x.heic", 32); !bytes.Equal(got, th) {
		t.Fatal("stored thumbnail not returned")
	}
	if err := ClearThumbCache(disk); err != nil || ThumbCacheSize(disk) != 0 {
		t.Fatal("not cleared")
	}
}

// Server talks to a client-decrypt server and never sends a vault password
// before logging in.
func TestServerClientDecryptOnly(t *testing.T) {
	var unlocks int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/info":
			w.Write([]byte(`{"mode":"client-decrypt"}`))
		case r.URL.Path == "/api/login":
			w.Write([]byte(`{"ok":true}`))
		case r.URL.Path == "/api/vaults":
			w.Write([]byte(`{"vaults":[{"id":"v1","name":"Photos"}]}`))
		case strings.HasSuffix(r.URL.Path, "/unlock"):
			unlocks++
			http.Error(w, "{}", http.StatusNotFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	s, err := NewServer(ts.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	if m, err := s.Mode(); err != nil || m != "client-decrypt" {
		t.Fatalf("Mode = %q, %v", m, err)
	}
	if _, err := s.OpenVault("v1", "secret", ""); err == nil {
		t.Fatal("opened before login")
	}
	if unlocks != 0 {
		t.Fatal("the vault password was sent to the server")
	}
	if err := s.Login("access"); err != nil {
		t.Fatal(err)
	}
	if js, err := s.Vaults(); err != nil || !strings.Contains(js, `"Photos"`) {
		t.Fatalf("Vaults = %s, %v", js, err)
	}
}

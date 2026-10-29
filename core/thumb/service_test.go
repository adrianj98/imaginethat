package thumb

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/adrianm/imagethat/core/cache"
	"github.com/adrianm/imagethat/core/vault"
)

const (
	fixtureDir      = "../vault/testdata/vault"
	fixturePassword = "test-password-123"
)

func openFixture(t *testing.T) *vault.Session {
	t.Helper()
	if _, err := os.Stat(filepath.Join(fixtureDir, "gocryptfs.conf")); err != nil {
		t.Skipf("fixture vault missing: %v", err)
	}
	s, err := vault.OpenLocal(fixtureDir, fixturePassword)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func decodeJPEG(t *testing.T, b []byte) image.Image {
	t.Helper()
	img, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("result is not a JPEG: %v", err)
	}
	return img
}

// countingReader wraps a Reader and counts how often the source is read, so
// tests can tell a cache hit from a re-render.
type countingReader struct {
	Reader
	reads atomic.Int32
}

func (c *countingReader) ReadFile(p string) ([]byte, error) {
	c.reads.Add(1)
	return c.Reader.ReadFile(p)
}

func (c *countingReader) Open(p string) (File, error) {
	c.reads.Add(1)
	return c.Reader.Open(p)
}

func TestGetRendersAndCaches(t *testing.T) {
	sess := openFixture(t)
	r := &countingReader{Reader: SessionReader{sess}}
	svc, err := New("fixture", r, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	for _, name := range []string{"photo.jpg", "photo.png"} {
		jpg, err := svc.Get(context.Background(), name, 64)
		if err != nil {
			t.Fatalf("Get(%s): %v", name, err)
		}
		img := decodeJPEG(t, jpg)
		if b := img.Bounds(); b.Dx() > 64 || b.Dy() > 64 {
			t.Errorf("%s: preview is %v, longest edge over 64", name, b)
		}
		before := r.reads.Load()
		again, err := svc.Get(context.Background(), name, 64)
		if err != nil || !bytes.Equal(again, jpg) {
			t.Errorf("%s: second Get differs or failed: %v", name, err)
		}
		if r.reads.Load() != before {
			t.Errorf("%s: second Get re-read the source (memory tier missed)", name)
		}
		// A different size is a different entry.
		small, err := svc.Get(context.Background(), name, 32)
		if err != nil || bytes.Equal(small, jpg) {
			t.Errorf("%s: 32px preview should differ: %v", name, err)
		}
	}
}

func TestGetNoPreviewCases(t *testing.T) {
	sess := openFixture(t)
	svc, err := New("fixture", SessionReader{sess}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	ctx := context.Background()

	for _, name := range []string{"large.bin", "clip.mp4", "subdir/nested.txt"} {
		if _, err := svc.Get(ctx, name, 128); !errors.Is(err, ErrNoPreview) {
			t.Errorf("Get(%s) = %v, want ErrNoPreview (no decoder)", name, err)
		}
	}
	if _, err := svc.Get(ctx, "missing.jpg", 128); err == nil || errors.Is(err, ErrNoPreview) {
		t.Errorf("Get(missing) = %v, want a not-found error, not ErrNoPreview", err)
	}
	if _, err := svc.Get(ctx, "photo.jpg", 8); err == nil {
		t.Error("Get with a tiny edge was accepted")
	}

	// A decodable image over the size cap is refused rather than decoded.
	capped, _ := New("fixture", SessionReader{sess}, Options{MaxSourceBytes: 100})
	defer capped.Close()
	if _, err := capped.Get(ctx, "photo.jpg", 64); !errors.Is(err, ErrNoPreview) {
		t.Errorf("oversized source: %v, want ErrNoPreview", err)
	}
}

// fakeDecoder stands in for the host's ImageIO/AVFoundation.
type fakeDecoder struct {
	images, videos atomic.Int32
	fail           bool
}

func (d *fakeDecoder) Image(data []byte, ext string, maxEdge int) ([]byte, error) {
	d.images.Add(1)
	if d.fail {
		return nil, errors.New("cannot decode")
	}
	return Make(data, maxEdge) // the fixture "RAW" below is really a PNG
}

func (d *fakeDecoder) Video(f File, ext string, maxEdge int) ([]byte, error) {
	d.videos.Add(1)
	if d.fail {
		return nil, errors.New("cannot decode")
	}
	// Prove the decoder got a readable range handle.
	buf := make([]byte, 16)
	if _, err := f.ReadAt(buf, 0); err != nil {
		return nil, err
	}
	solid := image.NewRGBA(image.Rect(0, 0, maxEdge, maxEdge/2))
	var out bytes.Buffer
	if err := jpeg.Encode(&out, solid, nil); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// renamingReader presents photo.png under a RAW extension so the "host
// decoder" path is exercised with real fixture bytes.
type renamingReader struct{ Reader }

func (r renamingReader) rewrite(p string) string {
	if p == "shot.dng" {
		return "photo.png"
	}
	return p
}
func (r renamingReader) FileSize(p string) (int64, error)  { return r.Reader.FileSize(r.rewrite(p)) }
func (r renamingReader) ReadFile(p string) ([]byte, error) { return r.Reader.ReadFile(r.rewrite(p)) }
func (r renamingReader) Open(p string) (File, error)       { return r.Reader.Open(r.rewrite(p)) }

func TestHostDecoderTiers(t *testing.T) {
	sess := openFixture(t)
	dec := &fakeDecoder{}
	svc, err := New("fixture", renamingReader{SessionReader{sess}}, Options{Decoder: dec})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	ctx := context.Background()

	jpg, err := svc.Get(ctx, "shot.dng", 48)
	if err != nil {
		t.Fatalf("Get(shot.dng) via host decoder: %v", err)
	}
	decodeJPEG(t, jpg)
	if dec.images.Load() != 1 {
		t.Errorf("host image decoder called %d times, want 1", dec.images.Load())
	}
	if _, err := svc.Get(ctx, "shot.dng", 48); err != nil || dec.images.Load() != 1 {
		t.Errorf("second Get re-decoded (calls=%d, err=%v)", dec.images.Load(), err)
	}

	frame, err := svc.Get(ctx, "clip.mp4", 64)
	if err != nil {
		t.Fatalf("Get(clip.mp4) via host video decoder: %v", err)
	}
	if b := decodeJPEG(t, frame).Bounds(); b.Dx() != 64 {
		t.Errorf("frame width %d, want 64", b.Dx())
	}
	if dec.videos.Load() != 1 {
		t.Errorf("video decoder called %d times", dec.videos.Load())
	}

	failing, _ := New("fixture2", renamingReader{SessionReader{sess}}, Options{Decoder: &fakeDecoder{fail: true}})
	defer failing.Close()
	if _, err := failing.Get(ctx, "clip.mp4", 64); !errors.Is(err, ErrNoPreview) {
		t.Errorf("failing decoder: %v, want ErrNoPreview", err)
	}
}

func TestPutAndInvalidate(t *testing.T) {
	sess := openFixture(t)
	r := &countingReader{Reader: SessionReader{sess}}
	svc, err := New("fixture", r, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	ctx := context.Background()

	// The host rendered a video frame itself; Put makes it a cache hit.
	hostFrame := []byte{0xFF, 0xD8, 0xFF, 0xE0, 1, 2, 3, 4}
	if err := svc.Put("clip.mp4", 100, hostFrame); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Get(ctx, "clip.mp4", 100)
	if err != nil || !bytes.Equal(got, hostFrame) {
		t.Fatalf("Get after Put: %v", err)
	}
	svc.Invalidate("clip.mp4")
	if _, err := svc.Get(ctx, "clip.mp4", 100); !errors.Is(err, ErrNoPreview) {
		t.Errorf("after Invalidate: %v, want ErrNoPreview (no decoder)", err)
	}
	if err := svc.Put("missing.jpg", 100, hostFrame); err == nil {
		t.Error("Put for a missing file succeeded")
	}
}

func TestDiskTierSurvivesNewService(t *testing.T) {
	sess := openFixture(t)
	disk := cache.NewThumbDisk(filepath.Join(t.TempDir(), "thumbs"), 10<<20)

	r1 := &countingReader{Reader: SessionReader{sess}}
	s1, err := New("fixture", r1, Options{Disk: disk, Sealer: sess})
	if err != nil {
		t.Fatal(err)
	}
	first, err := s1.Get(context.Background(), "photo.jpg", 96)
	if err != nil {
		t.Fatal(err)
	}
	s1.Close()

	// A fresh service (a relaunch) must find the preview on disk without
	// touching the source.
	r2 := &countingReader{Reader: SessionReader{sess}}
	s2, err := New("fixture", r2, Options{Disk: disk, Sealer: sess})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	second, err := s2.Get(context.Background(), "photo.jpg", 96)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("disk tier miss or mismatch: %v", err)
	}
	if r2.reads.Load() != 0 {
		t.Errorf("source read %d times on a disk hit", r2.reads.Load())
	}

	// Nothing on disk is plaintext.
	plain, _ := sess.ReadFile("photo.jpg")
	_ = filepath.WalkDir(disk.Root(), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, _ := os.ReadFile(p)
		if bytes.Contains(b, first[:32]) || bytes.Contains(b, plain[:32]) {
			t.Errorf("plaintext found in cache file %s", p)
		}
		return nil
	})

	// Without the session (vault locked) the disk entries are unreadable.
	sess.Close()
	if _, ok := disk.Get("fixture", cache.ThumbKey("photo.jpg", 96, int64(len(plain))), sess); ok {
		t.Error("disk entry opened after the session was closed")
	}

	if _, err := New("x", SessionReader{sess}, Options{Disk: disk}); err == nil {
		t.Error("New accepted a disk tier without a sealer")
	}
}

func TestConcurrencyLimitAndCancellation(t *testing.T) {
	sess := openFixture(t)
	gate := make(chan struct{})
	dec := &blockingDecoder{gate: gate, started: make(chan struct{})}
	svc, err := New("fixture", renamingReader{SessionReader{sess}}, Options{Decoder: dec, Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	// One decode holds the single permit...
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = svc.Get(context.Background(), "clip.mp4", 64)
	}()
	select {
	case <-dec.started:
	case <-time.After(5 * time.Second):
		t.Fatal("decoder never started")
	}

	// ...so a second request waits, and honours cancellation while waiting.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := svc.Get(ctx, "shot.dng", 64); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("waiting request: %v, want DeadlineExceeded", err)
	}
	if dec.images.Load() != 0 {
		t.Error("second decode ran despite the permit being held")
	}
	close(gate)
	wg.Wait()
}

type blockingDecoder struct {
	fakeDecoder
	gate    chan struct{}
	started chan struct{}
	once    sync.Once
}

func (d *blockingDecoder) Video(f File, ext string, maxEdge int) ([]byte, error) {
	d.once.Do(func() { close(d.started) })
	<-d.gate
	return d.fakeDecoder.Video(f, ext, maxEdge)
}

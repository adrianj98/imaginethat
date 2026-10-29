package vault

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Large video in a gocryptfs vault behind a slow link (NAS over Wi-Fi, a cloud
// drive, an Android SAF provider). The vault is written by this package, then
// reopened through slowVFS, which charges latency per backend call and time
// per byte, and counts both. Nothing above slowVFS is faked: the reads go
// through Session, File and (for the ffmpeg test) http.ServeContent, the same
// path the webserver, the Android app and the macOS shim use.
//
// Two kinds of cost are checked:
//   - modelled cost (calls*latency + bytes/bandwidth, no sleeping), so the
//     budget assertions are deterministic and fast on CI;
//   - wall-clock cost with real sleeps, for the ffmpeg/ffprobe test, which
//     uses a real demuxer as the "player".
//
// Env knobs:
//
//	IMAGETHAT_SLOW_VIDEO_MB=256    synthetic file size (64 under -short)
//	IMAGETHAT_SLOW_LATENCY_MS=20   latency charged per backend call
//	IMAGETHAT_SLOW_MBPS=10         link bandwidth in MB/s
//	IMAGETHAT_SKIP_SLOW_VIDEO=1    skip everything here

const slowPassword = "slow-video-password"

type link struct {
	latency time.Duration
	bps     float64 // bytes per second
}

func linkFromEnv() link {
	ms := envInt("IMAGETHAT_SLOW_LATENCY_MS", 20)
	mbps := envInt("IMAGETHAT_SLOW_MBPS", 10)
	return link{latency: time.Duration(ms) * time.Millisecond, bps: float64(mbps << 20)}
}

func (l link) cost(calls, bytes int64) time.Duration {
	return time.Duration(calls)*l.latency + time.Duration(float64(bytes)/l.bps*float64(time.Second))
}

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}

// slowVFS wraps a VFS and accounts for every backend call. It deliberately
// does not implement FileOpener: a network backend has no persistent handle,
// so every ciphertext read is a round trip.
type slowVFS struct {
	inner VFS
	link  link
	sleep bool // really sleep, for tests driven by an external process

	calls atomic.Int64
	bytes atomic.Int64
	// low counts bytes read below lowMark, to tell a seek from a download.
	low     atomic.Int64
	lowMark atomic.Int64
}

func (s *slowVFS) charge(n int) {
	s.calls.Add(1)
	s.bytes.Add(int64(n))
	if s.sleep {
		time.Sleep(s.link.cost(1, int64(n)))
	}
}

func (s *slowVFS) reset() { s.calls.Store(0); s.bytes.Store(0) }

func (s *slowVFS) modelled() time.Duration { return s.link.cost(s.calls.Load(), s.bytes.Load()) }

func (s *slowVFS) ReadAt(p string, off int64, buf []byte) (int, error) {
	n, err := s.inner.ReadAt(p, off, buf)
	if mark := s.lowMark.Load(); off < mark {
		s.low.Add(min(int64(n), mark-off))
	}
	s.charge(n)
	return n, err
}

func (s *slowVFS) FileSize(p string) (int64, error) {
	s.charge(0)
	return s.inner.FileSize(p)
}

func (s *slowVFS) ReadDir(p string) ([]DirEntry, error) {
	s.charge(0)
	return s.inner.ReadDir(p)
}

func (s *slowVFS) ReadFile(p string) ([]byte, error) {
	b, err := s.inner.ReadFile(p)
	s.charge(len(b))
	return b, err
}

// videoByte is the synthetic plaintext: computable at any offset, so ranges
// deep in a 256 MB file can be checked without holding it.
func videoByte(i int64) byte {
	x := uint64(i/8) * 0x9E3779B97F4A7C15
	x ^= x >> 29
	return byte(x >> (8 * (i % 8)))
}

func videoBytes(off int64, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = videoByte(off + int64(i))
	}
	return b
}

type videoReader struct{ pos, size int64 }

func (r *videoReader) Read(p []byte) (int, error) {
	if r.pos >= r.size {
		return 0, io.EOF
	}
	n := int(min(int64(len(p)), r.size-r.pos))
	for i := 0; i < n; i++ {
		p[i] = videoByte(r.pos + int64(i))
	}
	r.pos += int64(n)
	return n, nil
}

// slowVault creates a vault holding name (written by src) and reopens it
// behind a slowVFS.
func slowVault(t *testing.T, name string, src io.Reader, sleep bool) (*Session, *slowVFS) {
	t.Helper()
	if os.Getenv("IMAGETHAT_SKIP_SLOW_VIDEO") == "1" {
		t.Skip("IMAGETHAT_SKIP_SLOW_VIDEO=1")
	}
	dir := t.TempDir()
	local := NewLocalFS(dir)
	if err := Init(local, slowPassword); err != nil {
		t.Fatal(err)
	}
	w, err := OpenLocal(dir, slowPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Create(name, src); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	w.Close()

	slow := &slowVFS{inner: local, link: linkFromEnv(), sleep: sleep}
	s, err := Open(slow, slowPassword)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	slow.reset()
	return s, slow
}

func syntheticSize(t *testing.T) int64 {
	mb := envInt("IMAGETHAT_SLOW_VIDEO_MB", 256)
	if testing.Short() {
		mb = min(mb, 64)
	}
	return int64(mb) << 20
}

func TestSlowLinkLargeVideo(t *testing.T) {
	size := syntheticSize(t)
	s, slow := slowVault(t, "big.mp4", &videoReader{size: size}, false)
	l := slow.link
	t.Logf("%d MB video, %v per call, %.0f MB/s", size>>20, l.latency, l.bps/(1<<20))

	f, err := s.Open("big.mp4")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if f.Size() != size {
		t.Fatalf("Size = %d, want %d", f.Size(), size)
	}

	t.Run("byte-exact where players read", func(t *testing.T) {
		const bs = 4096
		for _, off := range []int64{0, bs - 7, bs, size/2 + 123, size - 300_000, size - 10} {
			n := int(min(256*1024, size-off))
			got := make([]byte, n)
			if _, err := f.ReadAt(got, off); err != nil && !errors.Is(err, io.EOF) {
				t.Fatalf("ReadAt(%d): %v", off, err)
			}
			if !bytes.Equal(got, videoBytes(off, n)) {
				t.Errorf("mismatch at offset %d", off)
			}
		}
		// Past the end: EOF, nothing pulled.
		slow.reset()
		if n, err := f.ReadAt(make([]byte, 16), size); n != 0 || !errors.Is(err, io.EOF) {
			t.Errorf("read past EOF = %d, %v", n, err)
		}
		if slow.calls.Load() != 0 {
			t.Errorf("read past EOF made %d backend calls", slow.calls.Load())
		}
	})

	// What a player does before the first frame of an MP4 whose moov atom is
	// at the end (ffmpeg's default): sniff the head, read the tail, then
	// jump to the first samples.
	t.Run("startup with moov at end", func(t *testing.T) {
		slow.reset()
		reads := []struct{ off, n int64 }{
			{0, 64 << 10},                 // ftyp + box headers
			{size - 1<<20, 1 << 20},       // moov
			{32 << 10, 512 << 10},         // first samples
			{size * 2 / 5, 512 << 10},     // a scrub to 40%
			{size*2/5 + 512<<10, 1 << 20}, // and keep playing
		}
		var requested int64
		for _, r := range reads {
			buf := make([]byte, r.n)
			if _, err := f.ReadAt(buf, r.off); err != nil && !errors.Is(err, io.EOF) {
				t.Fatalf("ReadAt(%d,%d): %v", r.off, r.n, err)
			}
			requested += r.n
		}
		pulled, calls, cost := slow.bytes.Load(), slow.calls.Load(), slow.modelled()
		floor := l.cost(int64(len(reads)), requested)
		t.Logf("requested %d KB, pulled %d KB in %d calls: %v on this link (floor %v)",
			requested>>10, pulled>>10, calls, cost.Round(time.Millisecond), floor.Round(time.Millisecond))

		// gocryptfs adds 32 bytes per 4 KiB block plus whole-block alignment
		// at each end of a read, so ~1% overhead is the format, not waste.
		if limit := requested + requested/50 + int64(len(reads))*2*4128; pulled > limit {
			t.Errorf("pulled %d bytes for %d requested (limit %d): reads are amplified", pulled, requested, limit)
		}
		// The budget is a few round trips per player request on top of the
		// bandwidth floor. One round trip per 4 KiB block would cost hundreds.
		if cost > floor*3/2+time.Duration(len(reads))*4*l.latency {
			t.Errorf("startup costs %v on a %v link (floor %v): too many round trips (%d calls)",
				cost.Round(time.Millisecond), l.latency, floor.Round(time.Millisecond), calls)
		}
		if pulled > size/3 {
			t.Errorf("startup pulled %d of %d bytes", pulled, size)
		}
	})

	// Straight-through playback in the 256 KiB requests the macOS resource
	// loader and the Android data source make: bounded memory, near the
	// bandwidth floor.
	t.Run("sequential playback", func(t *testing.T) {
		slow.reset()
		runtime.GC()
		var before runtime.MemStats
		runtime.ReadMemStats(&before)
		var peak uint64
		buf := make([]byte, 256<<10)
		var mismatch int64 = -1
		for off := int64(0); off < size; off += int64(len(buf)) {
			n, err := f.ReadAt(buf, off)
			if err != nil && !errors.Is(err, io.EOF) {
				t.Fatalf("ReadAt(%d): %v", off, err)
			}
			// Spot-check one byte per request rather than recomputing 256 MB.
			if mismatch < 0 && n > 0 && buf[n-1] != videoByte(off+int64(n)-1) {
				mismatch = off
			}
			if off%(32<<20) == 0 {
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				peak = max(peak, m.HeapInuse)
			}
		}
		if mismatch >= 0 {
			t.Errorf("content mismatch in request at %d", mismatch)
		}
		growth := int64(peak) - int64(before.HeapInuse)
		// The floor is one round trip per player request plus the bytes: a
		// player making serial requests pays that whatever the engine does.
		// What the engine must not add is extra round trips per request.
		requests := (size + int64(len(buf)) - 1) / int64(len(buf))
		cost, floor := slow.modelled(), l.cost(requests, size)
		t.Logf("streamed %d MB in %d requests: %d backend calls, %v on this link (floor %v), heap +%d KB",
			size>>20, requests, slow.calls.Load(), cost.Round(time.Millisecond), floor.Round(time.Millisecond), growth>>10)
		if growth > 32<<20 {
			t.Errorf("heap grew %d MB while streaming: the file is being buffered", growth>>20)
		}
		if calls := slow.calls.Load(); calls > requests*11/10 {
			t.Errorf("%d backend calls for %d player requests: reads are split into extra round trips", calls, requests)
		}
		if cost > floor*11/10 {
			t.Errorf("playback costs %v, over 1.1x the floor %v", cost.Round(time.Millisecond), floor.Round(time.Millisecond))
		}
	})

	// Players issue overlapping range requests from several threads.
	t.Run("concurrent range requests", func(t *testing.T) {
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		for g := 0; g < 8; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				rng := rand.New(rand.NewPCG(uint64(g), 7))
				for i := 0; i < 20; i++ {
					off := rng.Int64N(size)
					n := int(min(int64(1+rng.IntN(300_000)), size-off))
					got := make([]byte, n)
					if _, err := f.ReadAt(got, off); err != nil && !errors.Is(err, io.EOF) {
						errs <- fmt.Errorf("ReadAt(%d,%d): %w", off, n, err)
						return
					}
					if !bytes.Equal(got, videoBytes(off, n)) {
						errs <- fmt.Errorf("mismatch at %d+%d", off, n)
						return
					}
				}
			}(g)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Error(err)
		}
	})

	// Locking the vault while a player is mid-stream must fail the reads
	// cleanly, not panic or keep pulling.
	t.Run("lock mid-stream", func(t *testing.T) {
		s2, slow2 := slowVault(t, "lock.mp4", &videoReader{size: 8 << 20}, false)
		f2, err := s2.Open("lock.mp4")
		if err != nil {
			t.Fatal(err)
		}
		defer f2.Close()
		buf := make([]byte, 256<<10)
		if _, err := f2.ReadAt(buf, 0); err != nil {
			t.Fatal(err)
		}
		s2.Close()
		before := slow2.calls.Load()
		if _, err := f2.ReadAt(buf, 1<<20); !errors.Is(err, ErrClosed) {
			t.Errorf("read after lock = %v, want ErrClosed", err)
		}
		if slow2.calls.Load() != before {
			t.Error("read after lock still went to the backend")
		}
	})
}

// TestSlowLinkRealVideoWithFFmpeg serves a real MP4 (moov at the end, ~40 MB)
// out of the vault over HTTP with http.ServeContent, exactly as the webserver
// does, from a backend that really sleeps. ffprobe and ffmpeg are the player:
// they must find the duration and decode frames at the start and near the end
// without downloading the file.
func TestSlowLinkRealVideoWithFFmpeg(t *testing.T) {
	if testing.Short() {
		t.Skip("real-time slow-link test; run without -short")
	}
	ffmpeg, err1 := exec.LookPath("ffmpeg")
	ffprobe, err2 := exec.LookPath("ffprobe")
	if err1 != nil || err2 != nil {
		t.Skip("needs ffmpeg and ffprobe on PATH")
	}

	const seconds = 20
	src := filepath.Join(t.TempDir(), "clip.mp4")
	gen := exec.Command(ffmpeg, "-v", "error", "-y",
		"-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=1280x720:rate=30:duration=%d", seconds),
		"-f", "lavfi", "-i", fmt.Sprintf("anoisesrc=d=%d:c=pink", seconds),
		// noise keeps the bitrate up so the file is large for its length
		"-vf", "noise=alls=40:allf=t",
		"-c:v", "libx264", "-preset", "ultrafast", "-b:v", "16M", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-shortest", src)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg could not build the fixture (%v): %s", err, out)
	}
	plain, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if moov, mdat := bytes.Index(plain, []byte("moov")), bytes.Index(plain, []byte("mdat")); moov < mdat {
		t.Fatalf("fixture has moov before mdat (%d < %d); the test needs the tail-first case", moov, mdat)
	}

	s, slow := slowVault(t, "clip.mp4", bytes.NewReader(plain), true)
	size := int64(len(plain))
	t.Logf("%d MB video, %v per call, %.0f MB/s, real sleeps", size>>20, slow.link.latency, slow.link.bps/(1<<20))

	var ranges atomic.Int64
	var rangeLog struct {
		sync.Mutex
		seen []string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rg := r.Header.Get("Range"); rg != "" {
			ranges.Add(1)
			rangeLog.Lock()
			rangeLog.seen = append(rangeLog.seen, rg)
			rangeLog.Unlock()
		}
		f, err := s.Open("clip.mp4")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", "video/mp4")
		http.ServeContent(w, r, "clip.mp4", time.Time{}, f)
	}))
	defer srv.Close()
	url := srv.URL + "/clip.mp4"

	run := func(name string, timeout time.Duration, cmd string, args ...string) ([]byte, time.Duration) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		start := time.Now()
		out, err := exec.CommandContext(ctx, cmd, args...).Output()
		took := time.Since(start)
		if err != nil {
			var stderr string
			if ee, ok := err.(*exec.ExitError); ok {
				stderr = string(ee.Stderr)
			}
			t.Fatalf("%s failed after %v: %v %s", name, took.Round(time.Millisecond), err, stderr)
		}
		return out, took
	}

	slow.reset()
	out, took := run("ffprobe duration", 60*time.Second, ffprobe, "-v", "error",
		"-show_entries", "format=duration", "-of", "default=nw=1:nk=1", url)
	dur, _ := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	t.Logf("ffprobe: duration %.2fs in %v, %d KB pulled in %d calls",
		dur, took.Round(time.Millisecond), slow.bytes.Load()>>10, slow.calls.Load())
	if dur < seconds-1 || dur > seconds+1 {
		t.Errorf("duration = %.2f, want ~%d", dur, seconds)
	}
	if took > 10*time.Second {
		t.Errorf("finding the duration took %v over the slow link", took.Round(time.Millisecond))
	}
	// Reading the moov at the tail must not mean reading the file.
	if pulled := slow.bytes.Load(); pulled > size/10 {
		t.Errorf("ffprobe pulled %d of %d MB to find the duration", pulled>>20, size>>20)
	}

	// Ciphertext offsets in the first half of the file, for the late frame.
	slow.lowMark.Store(size / 2)
	for _, at := range []int{1, seconds - 2} {
		before := slow.bytes.Load()
		slow.low.Store(0)
		rangeLog.Lock()
		rangeLog.seen = nil
		rangeLog.Unlock()
		jpeg, took := run(fmt.Sprintf("frame at %ds", at), 90*time.Second, ffmpeg, "-v", "error",
			"-ss", strconv.Itoa(at), "-i", url, "-frames:v", "1", "-f", "image2pipe", "-vcodec", "mjpeg", "-")
		t.Logf("frame at %ds: %d bytes in %v, %d KB pulled", at, len(jpeg), took.Round(time.Millisecond),
			(slow.bytes.Load()-before)>>10)
		if !bytes.HasPrefix(jpeg, []byte{0xFF, 0xD8}) {
			t.Errorf("frame at %ds is not a JPEG", at)
		}
		if took > 20*time.Second {
			t.Errorf("frame at %ds took %v over the slow link", at, took.Round(time.Millisecond))
		}
		rangeLog.Lock()
		t.Logf("frame at %ds: ranges requested %v", at, rangeLog.seen)
		rangeLog.Unlock()
		// A frame near the end must come from a seek, not from reading
		// through the start of the file. ffmpeg's first request is
		// "bytes=0-" to read the head; until it hangs up to jump to the moov,
		// the server streams on into the socket buffers. That over-read is
		// bounded by the buffers (a few MB), not by the file, so the limit is
		// an eighth of the file: reading through would be about half.
		if at > seconds/2 {
			if low := slow.low.Load(); low > size/8 {
				t.Errorf("frame at %ds read %d KB from the first half of the file: it is not seeking", at, low>>10)
			} else {
				t.Logf("frame at %ds read %d KB from the first half of the file", at, low>>10)
			}
		}
	}

	if ranges.Load() == 0 {
		t.Error("ffmpeg made no Range requests; the test is not exercising seeks")
	}
	// No assertion on the total: ffmpeg asks for open-ended ranges
	// ("bytes=N-"), so the server keeps reading until the client hangs up and
	// the loopback socket buffers are full. That over-read is HTTP, not the
	// vault; the per-step checks above are what show seeking works.
	t.Logf("probe + 2 frames pulled %d of %d MB in total", slow.bytes.Load()>>20, size>>20)
}

package vault

import (
	"bytes"
	"errors"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOpenLocal(t *testing.T) {
	t.Run("correct password", func(t *testing.T) {
		s := openFixture(t)
		if s.plaintextNames {
			t.Error("fixture should use encrypted names")
		}
		if !s.useDirIV || !s.longNames {
			t.Errorf("fixture should have DirIV and LongNames: dirIV=%v longNames=%v", s.useDirIV, s.longNames)
		}
	})

	t.Run("wrong password", func(t *testing.T) {
		_, err := OpenLocal(fixtureDir, "not-the-password")
		if !errors.Is(err, ErrWrongPassword) {
			t.Fatalf("got %v, want ErrWrongPassword", err)
		}
	})

	t.Run("not a vault", func(t *testing.T) {
		_, err := OpenLocal(t.TempDir(), "irrelevant")
		if !errors.Is(err, ErrNoConfig) {
			t.Fatalf("got %v, want ErrNoConfig", err)
		}
	})
}

func TestIsVault(t *testing.T) {
	if !IsVault(fixtureDir) {
		t.Errorf("IsVault(%s) = false, want true", fixtureDir)
	}
	if IsVault(t.TempDir()) {
		t.Error("IsVault(empty dir) = true, want false")
	}
}

func TestListDirRoot(t *testing.T) {
	s := openFixture(t)
	entries, err := s.ListDir("")
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}

	got := make(map[string]Entry, len(entries))
	for _, e := range entries {
		got[e.Name] = e
	}

	// Every root-level fixture file must be listed at its plaintext size.
	for _, f := range fixtureFiles() {
		if strings.Contains(f.Path, "/") {
			continue
		}
		e, ok := got[f.Path]
		if !ok {
			t.Errorf("%s missing from root listing", f.Path)
			continue
		}
		if want := int64(len(f.Content())); e.Size != want {
			t.Errorf("%s: size = %d, want %d", f.Path, e.Size, want)
		}
		if e.IsDir {
			t.Errorf("%s: IsDir = true", f.Path)
		}
	}

	for _, dir := range []string{"subdir", "emptydir"} {
		e, ok := got[dir]
		if !ok {
			t.Errorf("%s missing from root listing", dir)
			continue
		}
		if !e.IsDir {
			t.Errorf("%s: IsDir = false", dir)
		}
	}

	// gocryptfs bookkeeping must never leak into a listing.
	for _, name := range []string{"gocryptfs.conf", "gocryptfs.diriv"} {
		if _, ok := got[name]; ok {
			t.Errorf("%s leaked into listing", name)
		}
	}
	for name := range got {
		if strings.HasPrefix(name, "gocryptfs.longname.") {
			t.Errorf("undecoded long name %q leaked into listing", name)
		}
	}

	// Directories sort before files.
	var seenFile bool
	for _, e := range entries {
		if !e.IsDir {
			seenFile = true
		} else if seenFile {
			t.Errorf("directory %q sorted after a file", e.Name)
		}
	}
}

func TestListDirNested(t *testing.T) {
	s := openFixture(t)
	entries, err := s.ListDir("subdir")
	if err != nil {
		t.Fatalf("ListDir(subdir): %v", err)
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name] = true
	}
	for _, want := range []string{"nested.txt", "deeper", longFixtureName} {
		if !names[want] {
			t.Errorf("subdir listing missing %q; got %v", want, names)
		}
	}

	empty, err := s.ListDir("emptydir")
	if err != nil {
		t.Fatalf("ListDir(emptydir): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("emptydir has %d entries, want 0", len(empty))
	}
}

func TestListDirNotFound(t *testing.T) {
	s := openFixture(t)
	if _, err := s.ListDir("does-not-exist"); err == nil {
		t.Fatal("expected an error for a missing directory")
	}
}

func TestMediaClassification(t *testing.T) {
	s := openFixture(t)
	entries, err := s.ListDir("")
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	want := map[string]MediaType{
		"photo.jpg": MediaImage,
		"photo.png": MediaImage,
		"clip.mp4":  MediaVideo,
		"large.bin": MediaOther,
		"empty.bin": MediaOther,
	}
	for _, e := range entries {
		if w, ok := want[e.Name]; ok && e.Media != w {
			t.Errorf("%s: mediaType = %q, want %q", e.Name, e.Media, w)
		}
	}
}

func TestFileSizeAndStat(t *testing.T) {
	s := openFixture(t)
	for _, f := range fixtureFiles() {
		want := int64(len(f.Content()))

		size, err := s.FileSize(f.Path)
		if err != nil {
			t.Errorf("FileSize(%s): %v", f.Path, err)
			continue
		}
		if size != want {
			t.Errorf("FileSize(%s) = %d, want %d", f.Path, size, want)
		}

		st, err := s.Stat(f.Path)
		if err != nil {
			t.Errorf("Stat(%s): %v", f.Path, err)
			continue
		}
		if st.Size != want {
			t.Errorf("Stat(%s).Size = %d, want %d", f.Path, st.Size, want)
		}
	}
}

// TestReadWholeFile is the core check: every byte the real gocryptfs binary
// wrote must come back identical through this package.
func TestReadWholeFile(t *testing.T) {
	s := openFixture(t)
	for _, f := range fixtureFiles() {
		want := f.Content()

		got, err := s.ReadFile(f.Path)
		if err != nil {
			t.Errorf("ReadFile(%s): %v", f.Path, err)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("ReadFile(%s): content mismatch (got %d bytes, want %d)", f.Path, len(got), len(want))
			continue
		}

		// The same content via io.Reader / io.Copy.
		fh, err := s.Open(f.Path)
		if err != nil {
			t.Errorf("Open(%s): %v", f.Path, err)
			continue
		}
		var buf bytes.Buffer
		if _, err := io.Copy(&buf, fh); err != nil {
			t.Errorf("io.Copy(%s): %v", f.Path, err)
		}
		if !bytes.Equal(buf.Bytes(), want) {
			t.Errorf("io.Copy(%s): content mismatch (got %d bytes, want %d)", f.Path, buf.Len(), len(want))
		}
		if fh.Size() != int64(len(want)) {
			t.Errorf("Size(%s) = %d, want %d", f.Path, fh.Size(), len(want))
		}
		fh.Close()
	}
}

// TestReadAtRandomRanges hammers the block arithmetic, which is where an
// off-by-one silently corrupts a video mid-scrub.
func TestReadAtRandomRanges(t *testing.T) {
	s := openFixture(t)
	rng := rand.New(rand.NewSource(1234))

	for _, f := range fixtureFiles() {
		want := f.Content()
		if len(want) == 0 {
			continue
		}
		fh, err := s.Open(f.Path)
		if err != nil {
			t.Fatalf("Open(%s): %v", f.Path, err)
		}

		for i := 0; i < 60; i++ {
			off := rng.Intn(len(want))
			length := rng.Intn(len(want)-off) + 1
			buf := make([]byte, length)
			n, err := fh.ReadAt(buf, int64(off))
			if err != nil && !errors.Is(err, io.EOF) {
				t.Fatalf("ReadAt(%s, off=%d, len=%d): %v", f.Path, off, length, err)
			}
			if n != length {
				t.Fatalf("ReadAt(%s, off=%d, len=%d): read %d bytes", f.Path, off, length, n)
			}
			if !bytes.Equal(buf, want[off:off+length]) {
				t.Fatalf("ReadAt(%s, off=%d, len=%d): bytes differ from plaintext", f.Path, off, length)
			}
		}
		fh.Close()
	}
}

// TestReadAtBlockBoundaries checks the exact offsets around the 4096-byte
// plaintext block size, where the range slicing is most fragile.
func TestReadAtBlockBoundaries(t *testing.T) {
	s := openFixture(t)
	const path = "large.bin"
	var want []byte
	for _, f := range fixtureFiles() {
		if f.Path == path {
			want = f.Content()
		}
	}

	fh, err := s.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer fh.Close()

	offsets := []int{0, 1, 4095, 4096, 4097, 8191, 8192, 8193, 12288, len(want) - 1}
	lengths := []int{1, 2, 4095, 4096, 4097, 8192}
	for _, off := range offsets {
		for _, length := range lengths {
			if off+length > len(want) {
				length = len(want) - off
			}
			if length <= 0 {
				continue
			}
			buf := make([]byte, length)
			n, err := fh.ReadAt(buf, int64(off))
			if err != nil && !errors.Is(err, io.EOF) {
				t.Fatalf("ReadAt(off=%d,len=%d): %v", off, length, err)
			}
			if n != length || !bytes.Equal(buf[:n], want[off:off+length]) {
				t.Errorf("ReadAt(off=%d,len=%d): mismatch (n=%d)", off, length, n)
			}
		}
	}
}

func TestReadAtPastEnd(t *testing.T) {
	s := openFixture(t)
	fh, err := s.Open("onebyte.bin")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer fh.Close()

	buf := make([]byte, 16)
	if _, err := fh.ReadAt(buf, 1); !errors.Is(err, io.EOF) {
		t.Errorf("ReadAt at EOF: got %v, want io.EOF", err)
	}
	n, err := fh.ReadAt(buf, 0)
	if n != 1 {
		t.Errorf("ReadAt(0) read %d bytes, want 1", n)
	}
	if !errors.Is(err, io.EOF) {
		t.Errorf("short ReadAt: got %v, want io.EOF", err)
	}
}

func TestEmptyFile(t *testing.T) {
	s := openFixture(t)
	fh, err := s.Open("empty.bin")
	if err != nil {
		t.Fatalf("Open(empty.bin): %v", err)
	}
	defer fh.Close()
	if fh.Size() != 0 {
		t.Errorf("Size = %d, want 0", fh.Size())
	}
	buf := make([]byte, 8)
	if _, err := fh.Read(buf); !errors.Is(err, io.EOF) {
		t.Errorf("Read on empty file: got %v, want io.EOF", err)
	}
}

func TestSeek(t *testing.T) {
	s := openFixture(t)
	const path = "block-8192.bin"
	var want []byte
	for _, f := range fixtureFiles() {
		if f.Path == path {
			want = f.Content()
		}
	}

	fh, err := s.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer fh.Close()

	// http.ServeContent determines length by seeking to the end.
	end, err := fh.Seek(0, io.SeekEnd)
	if err != nil {
		t.Fatalf("Seek(end): %v", err)
	}
	if end != int64(len(want)) {
		t.Errorf("Seek(0, SeekEnd) = %d, want %d", end, len(want))
	}

	if _, err := fh.Seek(4096, io.SeekStart); err != nil {
		t.Fatalf("Seek(start): %v", err)
	}
	buf := make([]byte, 100)
	if _, err := io.ReadFull(fh, buf); err != nil {
		t.Fatalf("ReadFull after seek: %v", err)
	}
	if !bytes.Equal(buf, want[4096:4196]) {
		t.Error("bytes after Seek do not match plaintext")
	}

	// SeekCurrent continues from where the read left off.
	pos, err := fh.Seek(0, io.SeekCurrent)
	if err != nil {
		t.Fatalf("Seek(current): %v", err)
	}
	if pos != 4196 {
		t.Errorf("position = %d, want 4196", pos)
	}
}

// TestServeContentRange proves the payoff of making *File an io.ReadSeeker:
// http.ServeContent handles Range requests with no extra code.
func TestServeContentRange(t *testing.T) {
	s := openFixture(t)
	const path = "clip.mp4"
	var want []byte
	for _, f := range fixtureFiles() {
		if f.Path == path {
			want = f.Content()
		}
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fh, err := s.Open(path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		defer fh.Close()
		http.ServeContent(w, r, path, time.Time{}, fh)
	})

	t.Run("full body", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if !bytes.Equal(rec.Body.Bytes(), want) {
			t.Errorf("body mismatch: got %d bytes, want %d", rec.Body.Len(), len(want))
		}
		if got := rec.Header().Get("Accept-Ranges"); got != "bytes" {
			t.Errorf("Accept-Ranges = %q, want %q", got, "bytes")
		}
	})

	t.Run("byte range", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Range", "bytes=10000-20000")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusPartialContent {
			t.Fatalf("status = %d, want 206", rec.Code)
		}
		if got, w := rec.Header().Get("Content-Range"), "bytes 10000-20000/60000"; got != w {
			t.Errorf("Content-Range = %q, want %q", got, w)
		}
		if !bytes.Equal(rec.Body.Bytes(), want[10000:20001]) {
			t.Error("ranged body does not match plaintext at that offset")
		}
	})

	t.Run("suffix range", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Range", "bytes=-500")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusPartialContent {
			t.Fatalf("status = %d, want 206", rec.Code)
		}
		if !bytes.Equal(rec.Body.Bytes(), want[len(want)-500:]) {
			t.Error("suffix range body does not match plaintext tail")
		}
	})
}

func TestLongNameRoundTrip(t *testing.T) {
	s := openFixture(t)
	// The name is stored as gocryptfs.longname.<hash> plus a .name sidecar;
	// both listing it and opening it by plaintext name must work.
	entries, err := s.ListDir("")
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	var found bool
	for _, e := range entries {
		if e.Name == longFixtureName {
			found = true
		}
	}
	if !found {
		t.Fatalf("long name not present in listing")
	}

	got, err := s.ReadFile(longFixtureName)
	if err != nil {
		t.Fatalf("ReadFile(long name): %v", err)
	}
	if !bytes.Equal(got, patternBytes(12, 5000)) {
		t.Error("long-name file content mismatch")
	}
}

func TestConcurrentReads(t *testing.T) {
	s := openFixture(t)
	want := patternBytes(7, 100000)

	fh, err := s.Open("large.bin")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer fh.Close()

	// ReadAt must be safe for concurrent use — http.ServeContent and multiple
	// browser range requests share one session.
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func(i int) {
			off := i * 10000
			buf := make([]byte, 5000)
			if _, err := fh.ReadAt(buf, int64(off)); err != nil && !errors.Is(err, io.EOF) {
				errs <- err
				return
			}
			if !bytes.Equal(buf, want[off:off+5000]) {
				errs <- errors.New("concurrent ReadAt returned wrong bytes")
				return
			}
			errs <- nil
		}(i)
	}
	for i := 0; i < 8; i++ {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
}

func TestClosedSession(t *testing.T) {
	s, err := OpenLocal(fixtureDir, fixturePassword)
	if err != nil {
		t.Fatalf("OpenLocal: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := s.ListDir(""); !errors.Is(err, ErrClosed) {
		t.Errorf("ListDir after Close: got %v, want ErrClosed", err)
	}
	if _, err := s.Open("large.bin"); !errors.Is(err, ErrClosed) {
		t.Errorf("Open after Close: got %v, want ErrClosed", err)
	}
	// Close must be idempotent — the HTTP layer closes on both idle expiry
	// and explicit lock.
	if err := s.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

package mobile

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/adrianm/imagethat/webserver/vault"
)

const fixtureDir = "../vault/testdata/vault"
const fixturePassword = "test-password-123"

func newVault(t *testing.T) (*Vault, string) {
	t.Helper()
	dir := t.TempDir()
	if err := InitLocal(dir, "pw"); err != nil {
		t.Fatal(err)
	}
	v, err := OpenLocal(dir, "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { v.Close() })
	return v, dir
}

func put(t *testing.T, v *Vault, path string, data []byte) {
	t.Helper()
	w, err := v.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func names(t *testing.T, v *Vault, path string) []string {
	t.Helper()
	js, err := v.List(path)
	if err != nil {
		t.Fatal(err)
	}
	var es []vault.Entry
	if err := json.Unmarshal([]byte(js), &es); err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, e := range es {
		out = append(out, e.Name)
	}
	return out
}

func TestRoundTrip(t *testing.T) {
	v, _ := newVault(t)
	if got := names(t, v, ""); len(got) != 0 {
		t.Fatalf("new vault not empty: %v", got)
	}
	if err := v.Mkdir("d"); err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("abc"), 5000)
	put(t, v, "d/a.txt", data)
	if got := names(t, v, "d"); len(got) != 1 || got[0] != "a.txt" {
		t.Fatalf("list: %v", got)
	}
	if n, err := v.Size("d/a.txt"); err != nil || n != int64(len(data)) {
		t.Fatalf("size %d %v", n, err)
	}
	got, err := v.ReadRange("d/a.txt", 0, len(data))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("read mismatch: %v", err)
	}
	if err := v.Move("d/a.txt", "b.txt"); err != nil {
		t.Fatal(err)
	}
	if got := names(t, v, "d"); len(got) != 0 {
		t.Fatalf("after move: %v", got)
	}
	if err := v.Remove("b.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Size("b.txt"); err == nil || !strings.HasPrefix(err.Error(), "not_found:") {
		t.Fatalf("want not_found, got %v", err)
	}
}

func TestReadRange(t *testing.T) {
	v, _ := newVault(t)
	data := make([]byte, 9000)
	for i := range data {
		data[i] = byte(i * 7)
	}
	put(t, v, "f", data)
	for _, off := range []int64{0, 4095, 4096} {
		got, err := v.ReadRange("f", off, 100)
		if err != nil || !bytes.Equal(got, data[off:off+100]) {
			t.Fatalf("off %d: err %v", off, err)
		}
	}
	got, err := v.ReadRange("f", 8950, 500)
	if err != nil || !bytes.Equal(got, data[8950:]) {
		t.Fatalf("short read: %d bytes, %v", len(got), err)
	}
	got, err = v.ReadRange("f", 20000, 10)
	if err != nil || len(got) != 0 {
		t.Fatalf("past EOF: %d bytes, %v", len(got), err)
	}
}

func TestErrorCodes(t *testing.T) {
	dir := t.TempDir()
	if _, err := OpenLocal(dir, "x"); err == nil || !strings.HasPrefix(err.Error(), "no_config:") {
		t.Fatalf("no_config: %v", err)
	}
	if err := InitLocal(dir, "pw"); err != nil {
		t.Fatal(err)
	}
	if err := InitLocal(dir, "pw"); err == nil || !strings.HasPrefix(err.Error(), "exists:") {
		t.Fatalf("exists: %v", err)
	}
	if _, err := OpenLocal(dir, "bad"); err == nil || !strings.HasPrefix(err.Error(), "wrong_password:") {
		t.Fatalf("wrong_password: %v", err)
	}
	v, err := OpenLocal(dir, "pw")
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	if _, err := v.List("../x"); err == nil || !strings.HasPrefix(err.Error(), "invalid_path:") {
		t.Fatalf("invalid_path: %v", err)
	}
	if err := v.Mkdir("d"); err != nil {
		t.Fatal(err)
	}
	if err := v.Mkdir("d"); err == nil || !strings.HasPrefix(err.Error(), "exists:") {
		t.Fatalf("exists: %v", err)
	}
}

func TestFileWriterBoundedMemory(t *testing.T) {
	v, _ := newVault(t)
	chunk := make([]byte, 256<<10)
	for i := range chunk {
		chunk[i] = byte(i)
	}
	runtime.GC()
	var base, ms runtime.MemStats
	runtime.ReadMemStats(&base)
	peak := base.HeapInuse

	w, err := v.Create("big.bin")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 256; i++ { // 64 MiB
		if err := w.Write(chunk); err != nil {
			t.Fatal(err)
		}
		if i%8 == 0 {
			runtime.ReadMemStats(&ms)
			if ms.HeapInuse > peak {
				peak = ms.HeapInuse
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&ms)
	if ms.HeapInuse > peak {
		peak = ms.HeapInuse
	}
	if growth := int64(peak) - int64(base.HeapInuse); growth > 16<<20 {
		t.Fatalf("heap grew %d MiB writing 64 MiB", growth>>20)
	}
	if n, err := v.Size("big.bin"); err != nil || n != 64<<20 {
		t.Fatalf("size %d %v", n, err)
	}
}

func TestFileWriterAbort(t *testing.T) {
	v, dir := newVault(t)
	w, err := v.Create("partial")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Write(make([]byte, 100000)); err != nil {
		t.Fatal(err)
	}
	if err := w.Abort(); err != nil {
		t.Fatal(err)
	}
	if got := names(t, v, ""); len(got) != 0 {
		t.Fatalf("entry left behind: %v", got)
	}
	des, _ := os.ReadDir(dir)
	for _, de := range des {
		if strings.HasPrefix(de.Name(), ".imagethat-upload-") {
			t.Fatalf("temp file left: %s", de.Name())
		}
	}

	// Aborting a write to an existing path must not delete that file.
	put(t, v, "keep", []byte("x"))
	w, _ = v.Create("keep")
	_ = w.Abort()
	if n, err := v.Size("keep"); err != nil || n != 1 {
		t.Fatalf("existing file damaged: %d %v", n, err)
	}
}

func TestFixtureMatchesVault(t *testing.T) {
	if _, err := os.Stat(filepath.Join(fixtureDir, "gocryptfs.conf")); err != nil {
		t.Skip("fixture missing")
	}
	v, err := OpenLocal(fixtureDir, fixturePassword)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	s, err := vault.OpenLocal(fixtureDir, fixturePassword)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	var walk func(dir string) int
	walk = func(dir string) int {
		want, err := s.ListDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		js, err := v.List(dir)
		if err != nil {
			t.Fatal(err)
		}
		var got []vault.Entry
		if err := json.Unmarshal([]byte(js), &got); err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want) {
			t.Fatalf("%q: %d vs %d entries", dir, len(got), len(want))
		}
		files := 0
		for i, e := range want {
			if got[i] != e {
				t.Fatalf("entry %d in %q: %+v vs %+v", i, dir, got[i], e)
			}
			p := dir + "/" + e.Name
			if e.IsDir {
				files += walk(p)
				continue
			}
			files++
			f, err := s.Open(p)
			if err != nil {
				t.Fatal(err)
			}
			buf := make([]byte, f.Size())
			if _, err := f.ReadAt(buf, 0); err != nil && f.Size() > 0 {
				t.Fatal(err)
			}
			f.Close()
			b, err := v.ReadRange(p, 0, len(buf)+10)
			if err != nil || !bytes.Equal(b, buf) {
				t.Fatalf("%s mismatch: %v", p, err)
			}
		}
		return files
	}
	if n := walk(""); n == 0 {
		t.Fatal("fixture has no files")
	}
}

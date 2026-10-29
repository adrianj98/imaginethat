package mobile

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adrianm/imagethat/core/vault"
)

// dirStorage is a test Storage over a plain directory. It deliberately does
// not implement vault.FileOpener, so the plain ReadAt path is exercised.
type dirStorage struct{ root string }

type dirWriter struct{ f *os.File }

func (w dirWriter) Write(p []byte) error { _, err := w.f.Write(p); return err }
func (w dirWriter) Close() error         { return w.f.Close() }

func (d dirStorage) abs(p string) string { return filepath.Join(d.root, p) }

func (d dirStorage) ReadAt(path string, offset int64, n int) ([]byte, error) {
	f, err := os.Open(d.abs(path))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, n)
	got, err := f.ReadAt(buf, offset)
	if err != nil && err != io.EOF {
		return nil, err
	}
	return buf[:got], nil
}

func (d dirStorage) FileSize(path string) (int64, error) {
	fi, err := os.Stat(d.abs(path))
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

func (d dirStorage) ListDir(path string) (string, error) {
	des, err := os.ReadDir(d.abs(path))
	if err != nil {
		return "", err
	}
	type ent struct {
		Name  string `json:"name"`
		IsDir bool   `json:"isDir"`
		Size  int64  `json:"size"`
	}
	out := []ent{}
	for _, de := range des {
		var size int64
		if info, err := de.Info(); err == nil {
			size = info.Size()
		}
		out = append(out, ent{de.Name(), de.IsDir(), size})
	}
	b, err := json.Marshal(out)
	return string(b), err
}

func (d dirStorage) WriteFile(path string, data []byte) error {
	return os.WriteFile(d.abs(path), data, 0o600)
}

func (d dirStorage) OpenWriter(path string) (StorageWriter, error) {
	f, err := os.OpenFile(d.abs(path), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	return dirWriter{f}, nil
}

func (d dirStorage) Mkdir(path string) error     { return os.Mkdir(d.abs(path), 0o700) }
func (d dirStorage) Remove(path string) error    { return os.Remove(d.abs(path)) }
func (d dirStorage) RemoveAll(path string) error { return os.RemoveAll(d.abs(path)) }
func (d dirStorage) Rename(from, to string) error {
	return os.Rename(d.abs(from), d.abs(to))
}
func (d dirStorage) Exists(path string) bool {
	_, err := os.Lstat(d.abs(path))
	return err == nil
}

func newStorageVault(t *testing.T) (*Vault, string) {
	t.Helper()
	dir := t.TempDir()
	if err := InitStorage(dirStorage{dir}, "pw"); err != nil {
		t.Fatal(err)
	}
	v, err := OpenStorage(dirStorage{dir}, "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { v.Close() })
	return v, dir
}

func TestStorageRoundTrip(t *testing.T) {
	v, dir := newStorageVault(t)
	if got := names(t, v, ""); len(got) != 0 {
		t.Fatalf("new vault not empty: %v", got)
	}
	if err := v.Mkdir("d"); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 15000)
	for i := range data {
		data[i] = byte(i * 7)
	}
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
	for _, off := range []int64{4095, 4096} {
		got, err := v.ReadRange("d/a.txt", off, 100)
		if err != nil || !bytes.Equal(got, data[off:off+100]) {
			t.Fatalf("off %d: err %v", off, err)
		}
	}
	got, err = v.ReadRange("d/a.txt", 14950, 500)
	if err != nil || !bytes.Equal(got, data[14950:]) {
		t.Fatalf("short read: %d bytes, %v", len(got), err)
	}
	got, err = v.ReadRange("d/a.txt", 20000, 10)
	if err != nil || len(got) != 0 {
		t.Fatalf("past EOF: %d bytes, %v", len(got), err)
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
	// No temp files left in the vault directory.
	des, _ := os.ReadDir(dir)
	for _, de := range des {
		if strings.HasPrefix(de.Name(), ".tmp-") {
			t.Fatalf("temp file left: %s", de.Name())
		}
	}
}

func TestStorageErrorCodes(t *testing.T) {
	dir := t.TempDir()
	s := dirStorage{dir}
	if _, err := OpenStorage(s, "x"); err == nil || !strings.HasPrefix(err.Error(), "no_config:") {
		t.Fatalf("no_config: %v", err)
	}
	if err := InitStorage(s, "pw"); err != nil {
		t.Fatal(err)
	}
	if err := InitStorage(s, "pw"); err == nil || !strings.HasPrefix(err.Error(), "exists:") {
		t.Fatalf("exists: %v", err)
	}
	if _, err := OpenStorage(s, "bad"); err == nil || !strings.HasPrefix(err.Error(), "wrong_password:") {
		t.Fatalf("wrong_password: %v", err)
	}
}

func TestStorageAbort(t *testing.T) {
	v, dir := newStorageVault(t)
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
		if strings.HasPrefix(de.Name(), ".tmp-") || strings.HasPrefix(de.Name(), ".imagethat-upload-") {
			t.Fatalf("temp file left: %s", de.Name())
		}
	}
}

func TestStorageLongName(t *testing.T) {
	v, _ := newStorageVault(t)
	name := strings.Repeat("n", 180) + ".txt"
	if len(name) <= 175 {
		t.Fatal("name too short")
	}
	data := []byte("long name payload")
	put(t, v, name, data)
	if got := names(t, v, ""); len(got) != 1 || got[0] != name {
		t.Fatalf("list: %v", got)
	}
	got, err := v.ReadRange(name, 0, 100)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("read: %q %v", got, err)
	}
}

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if info.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o700)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestStorageFixtures(t *testing.T) {
	for _, name := range []string{"vault", "vault-aessiv", "vault-xchacha"} {
		t.Run(name, func(t *testing.T) {
			src := filepath.Join("../vault/testdata", name)
			if _, err := os.Stat(filepath.Join(src, "gocryptfs.conf")); err != nil {
				t.Skip("fixture missing")
			}
			dir := t.TempDir()
			copyDir(t, src, dir)
			v, err := OpenStorage(dirStorage{dir}, fixturePassword)
			if err != nil {
				t.Fatal(err)
			}
			defer v.Close()
			s, err := vault.OpenLocal(dir, fixturePassword)
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
		})
	}
}

// msgStorage wraps dirStorage but, like a Kotlin Storage across gomobile,
// reports failures as plain message errors following the documented contract.
type msgStorage struct{ dirStorage }

func msgErr(err error) error {
	switch {
	case err == nil:
		return nil
	case os.IsNotExist(err):
		return errors.New("not_found: " + err.Error())
	case os.IsExist(err):
		return errors.New("exists: " + err.Error())
	}
	return errors.New(err.Error())
}

func (m msgStorage) ReadAt(p string, o int64, n int) ([]byte, error) {
	b, err := m.dirStorage.ReadAt(p, o, n)
	return b, msgErr(err)
}
func (m msgStorage) FileSize(p string) (int64, error) {
	n, err := m.dirStorage.FileSize(p)
	return n, msgErr(err)
}
func (m msgStorage) ListDir(p string) (string, error) {
	s, err := m.dirStorage.ListDir(p)
	return s, msgErr(err)
}
func (m msgStorage) Mkdir(p string) error { return msgErr(m.dirStorage.Mkdir(p)) }
func (m msgStorage) OpenWriter(p string) (StorageWriter, error) {
	w, err := m.dirStorage.OpenWriter(p)
	return w, msgErr(err)
}

func TestStorageMessageErrors(t *testing.T) {
	dir := t.TempDir()
	s := msgStorage{dirStorage{dir}}
	if _, err := OpenStorage(s, "x"); err == nil || !strings.HasPrefix(err.Error(), "no_config:") {
		t.Fatalf("no_config: %v", err)
	}
	if err := InitStorage(s, "pw"); err != nil {
		t.Fatal(err)
	}
	v, err := OpenStorage(s, "pw")
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	if _, err := v.List("nope"); err == nil || !strings.HasPrefix(err.Error(), "not_found:") {
		t.Fatalf("List: %v", err)
	}
	if _, err := v.Size("nope"); err == nil || !strings.HasPrefix(err.Error(), "not_found:") {
		t.Fatalf("Size: %v", err)
	}
	if _, err := v.ReadRange("nope", 0, 10); err == nil || !strings.HasPrefix(err.Error(), "not_found:") {
		t.Fatalf("ReadRange: %v", err)
	}
	if err := v.Mkdir("d"); err != nil {
		t.Fatal(err)
	}
	if err := v.Mkdir("d"); err == nil || !strings.HasPrefix(err.Error(), "exists:") {
		t.Fatalf("Mkdir exists: %v", err)
	}
	put(t, v, "f", []byte("hi"))
	if got, err := v.ReadRange("f", 0, 10); err != nil || string(got) != "hi" {
		t.Fatalf("read: %q %v", got, err)
	}
}

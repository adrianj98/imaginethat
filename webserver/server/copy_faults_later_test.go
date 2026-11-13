package server

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/adrianm/imagethat/core/faulttest"
	"github.com/adrianm/imagethat/core/vault"
	"github.com/adrianm/imagethat/core/xfer"
)

// Scenarios that today's copies fail, waiting on later pieces of the copy
// fault-tolerance work. They are skipped; IMAGETHAT_RUN_LATER=1 runs them,
// and the piece that fixes one removes its skip.

func later(piece string) string {
	if os.Getenv("IMAGETHAT_RUN_LATER") == "1" {
		return ""
	}
	return "waits on " + piece + " (IMAGETHAT_RUN_LATER=1 runs it)"
}

// A source that hands back a wrong byte once, with no error (bad memory, a
// NAS returning garbage), must not end in a "successful" copy of the wrong
// bytes. Checking the bytes after the copy would catch it.
func TestCopyFaultsLaterSilentCorruption(t *testing.T) {
	if why := later("piece 2: content verification"); why != "" {
		t.Skip(why)
	}
	want := payload(faultSize)
	src := writeSource(t, want)
	dir := newLocalVault(t)
	s, err := unlockLocal(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Mkdir(filepath.Dir(destPath)); err != nil {
		t.Fatal(err)
	}
	corrupt := &flipOnce{at: faultSize / 2}
	open := func() (xfer.Source, error) {
		f, err := xfer.OpenFile(src)()
		if err != nil {
			return nil, err
		}
		return flipSource{f, corrupt}, nil
	}
	_, err = s.CopyIn(context.Background(), destPath, open, faultOpts())
	if !corrupt.done {
		t.Fatal("the corrupt read never happened")
	}
	got, readErr := s.ReadFile(destPath)
	if err == nil && (readErr != nil || !bytes.Equal(got, want)) {
		t.Errorf("the copy reported success with wrong bytes at the destination")
	}
}

// flipOnce flips one byte the first time a read covers it.
type flipOnce struct {
	at   int64
	done bool
}

type flipSource struct {
	xfer.Source
	f *flipOnce
}

func (s flipSource) ReadAt(p []byte, off int64) (int, error) {
	n, err := s.Source.ReadAt(p, off)
	if !s.f.done && off <= s.f.at && s.f.at < off+int64(n) {
		p[s.f.at-off] ^= 0xff
		s.f.done = true
	}
	return n, err
}

// A retry that finds a file of the right size at the destination counts the
// copy as done, taking it for its own commit whose reply was lost. A file of
// the same size that came from somewhere else must not pass for it.
func TestCopyFaultsLaterSameSizeImpostor(t *testing.T) {
	if why := later("piece 2: content verification"); why != "" {
		t.Skip(why)
	}
	want := payload(faultSize)
	src := writeSource(t, want)
	dir := newLocalVault(t)
	impostor, err := unlockLocal(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer impostor.Close()
	if err := impostor.Mkdir(filepath.Dir(destPath)); err != nil {
		t.Fatal(err)
	}

	// The first commit fails, and in that moment another device writes a
	// different file of the same size under the same name.
	store := &impostorFS{WriteVFS: vault.NewLocalFS(dir), plant: func() {
		other := bytes.Repeat([]byte{7}, faultSize)
		if _, err := impostor.Create(destPath, bytes.NewReader(other)); err != nil {
			t.Errorf("planting the other file: %v", err)
		}
	}}
	s, err := unlock(t, store)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	_, err = s.CopyIn(context.Background(), destPath, xfer.OpenFile(src), faultOpts())
	if !store.planted {
		t.Fatal("the other file was never planted")
	}
	got, readErr := impostor.ReadFile(destPath)
	if err == nil && (readErr != nil || !bytes.Equal(got, want)) {
		t.Errorf("the copy reported success while the destination holds another file")
	}
}

type impostorFS struct {
	vault.WriteVFS
	plant   func()
	planted bool
}

func (f *impostorFS) Rename(oldPath, newPath string) error {
	if !f.planted {
		f.planted = true
		f.plant()
		return syscall.EIO
	}
	return f.WriteVFS.Rename(oldPath, newPath)
}

// An upload cut at 90% should send the last 10% again, not the whole file.
func TestCopyFaultsLaterUploadResumes(t *testing.T) {
	var before int64
	runScenario(t, serverUp, faultSize, scenario{
		name: "cut at 90% resumes",
		skip: later("piece 3: resumable uploads"),
		arm: func(_ *testing.T, r *rig) {
			before = r.proxy.Bytes(faulttest.Up)
			r.proxy.Arm(faulttest.Fault{Dir: faulttest.Up, At: faultSize * 9 / 10, Action: faulttest.Reset})
		},
		want: recovers,
		check: func(t *testing.T, r *rig) {
			if sent := r.proxy.Bytes(faulttest.Up) - before; sent > faultSize*12/10 {
				t.Errorf("sent %d bytes for a %d-byte file, want under %d", sent, faultSize, faultSize*12/10)
			}
		},
	})
}

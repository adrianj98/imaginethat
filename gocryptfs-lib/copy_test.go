//go:build shimtest

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestCopyFileInAndVaultIn(t *testing.T) {
	a, dirA := openCopy(t)
	b, _ := openCopy(t)
	want := pattern(5, 200_000)
	src := filepath.Join(t.TempDir(), "photo.jpg")
	if err := os.WriteFile(src, want, 0o600); err != nil {
		t.Fatal(err)
	}

	j := tJobNew()
	defer tJobFree(j)
	if rc := tCopyFileIn(j, a, "imported.jpg", src); rc != 0 {
		t.Fatalf("CopyFileIn = %d", rc)
	}
	if done, total, _, rc := tJobProgress(j); rc != 0 || done != int64(len(want)) || total != int64(len(want)) {
		t.Fatalf("progress = %d/%d rc %d", done, total, rc)
	}
	got, err := tDecryptFile(a, "imported.jpg")
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("read back: %v, equal %v", err, bytes.Equal(got, want))
	}
	assertNoPlaintext(t, dirA, want[:4096])

	if rc := tCopyFileIn(j, a, "imported.jpg", src); rc != rcExists {
		t.Fatalf("copy onto an existing file = %d, want %d", rc, rcExists)
	}
	if rc := tCopyFileIn(j, a, "missing.jpg", src+".nope"); rc == 0 {
		t.Fatal("copy of a missing file succeeded")
	}

	if rc := tCopyVaultIn(j, b, "from-a.jpg", a, "imported.jpg"); rc != 0 {
		t.Fatalf("CopyVaultIn = %d", rc)
	}
	got, err = tDecryptFile(b, "from-a.jpg")
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("vault copy: %v, equal %v", err, bytes.Equal(got, want))
	}
}

func TestCopyCancelled(t *testing.T) {
	a, _ := openCopy(t)
	src := filepath.Join(t.TempDir(), "big.bin")
	os.WriteFile(src, pattern(6, 100_000), 0o600)
	j := tJobNew()
	defer tJobFree(j)
	tJobCancel(j)
	if rc := tCopyFileIn(j, a, "big.bin", src); rc != rcCancelled {
		t.Fatalf("cancelled copy = %d, want %d", rc, rcCancelled)
	}
	if tFileSize(a, "big.bin") >= 0 {
		t.Fatal("a cancelled copy left a file")
	}
}

func TestCacheFileOnLocalSessionReadsThrough(t *testing.T) {
	a, _ := openCopy(t)
	if rc := tWriteFile(a, "x.bin", pattern(7, 30_000)); rc != 0 {
		t.Fatal(rc)
	}
	j := tJobNew()
	defer tJobFree(j)
	if rc := tCacheFile(j, a, "x.bin"); rc != 0 {
		t.Fatalf("CacheFile = %d", rc)
	}
	if tIsCached(a, "x.bin") {
		t.Fatal("a local session has no file cache")
	}
	if rc := tUncache(a, "x.bin"); rc != 0 {
		t.Fatalf("Uncache = %d", rc)
	}
}

package cache

import (
	"bytes"
	"testing"
	"time"
)

func TestRangesReadWriteMerge(t *testing.T) {
	r, err := NewRanges(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("0123456789"), 10) // 100 bytes
	if err := r.Write("f", "v1", 100, 0, data[:40]); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 20)
	if _, ok := r.ReadAt("f", "v1", 30, buf); ok {
		t.Fatal("read past held range")
	}
	r.Write("f", "v1", 100, 40, data[40:100])
	if iv := r.Intervals("f", "v1"); len(iv) != 1 || iv[0] != (Interval{0, 100}) {
		t.Fatalf("intervals = %v", iv)
	}
	if n, ok := r.ReadAt("f", "v1", 30, buf); !ok || n != 20 || !bytes.Equal(buf, data[30:50]) {
		t.Fatalf("read = %d %v", n, ok)
	}
	// A read running past the end is served up to the end.
	if n, ok := r.ReadAt("f", "v1", 90, buf); !ok || n != 10 {
		t.Fatalf("tail read = %d %v", n, ok)
	}
	if !r.Complete("f", "v1") || r.Size() != 100 {
		t.Fatalf("complete=%v size=%d", r.Complete("f", "v1"), r.Size())
	}
	// Another version drops the entry.
	if _, ok := r.ReadAt("f", "v2", 0, buf); ok || r.Size() != 0 {
		t.Fatal("stale version served")
	}
}

func TestRangesEvictionSparesPinned(t *testing.T) {
	dir := t.TempDir()
	r, _ := NewRanges(dir, 250)
	r.Pin("keep", true)
	r.Write("keep", "v", 100, 0, make([]byte, 100))
	r.Write("old", "v", 100, 0, make([]byte, 100))
	time.Sleep(time.Millisecond)
	r.Write("new", "v", 100, 0, make([]byte, 100))
	if r.Complete("old", "v") || !r.Complete("keep", "v") || !r.Complete("new", "v") {
		t.Fatal("wrong entry evicted")
	}

	// The index survives a restart.
	r2, _ := NewRanges(dir, 250)
	if !r2.Complete("keep", "v") || !r2.Pinned("keep") {
		t.Fatal("index not reloaded")
	}
	r2.Clear()
	if r2.Size() != 0 {
		t.Fatal("not cleared")
	}
}

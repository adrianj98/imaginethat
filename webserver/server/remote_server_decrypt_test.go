package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/adrianm/imagethat/core/remote"
)

// The Go client's server-decrypt API (used by the Mac and Android apps for
// browsing, uploads and edits) against the real server.
func TestRemoteServerDecryptAPI(t *testing.T) {
	srv, _ := newTestServer(t)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	ctx := context.Background()

	rc, err := remote.New(ts.URL, remote.Options{})
	if err != nil {
		t.Fatal(err)
	}
	vaults, err := rc.Vaults(ctx)
	if err != nil || len(vaults) != 1 || vaults[0].Unlocked {
		t.Fatalf("Vaults = %+v, %v", vaults, err)
	}
	id := vaults[0].ID

	if _, err := rc.List(ctx, id, ""); !errors.Is(err, remote.ErrLocked) {
		t.Fatalf("list while locked: %v", err)
	}
	if err := rc.Unlock(ctx, id, "wrong"); !errors.Is(err, remote.ErrWrongPassword) {
		t.Fatalf("wrong password: %v", err)
	}
	if err := rc.Unlock(ctx, id, fixturePassword); err != nil {
		t.Fatal(err)
	}
	if vaults, _ := rc.Vaults(ctx); !vaults[0].Unlocked {
		t.Fatal("not shown unlocked")
	}

	entries, err := rc.List(ctx, id, "")
	if err != nil || len(entries) == 0 {
		t.Fatalf("List = %v, %v", entries, err)
	}

	// Upload, read back whole and in ranges, mkdir, move, delete.
	data := bytes.Repeat([]byte("server decrypt "), 50_000)
	if err := rc.Mkdir(ctx, id, "Up"); err != nil {
		t.Fatal(err)
	}
	if err := rc.Mkdir(ctx, id, "Up"); !errors.Is(err, remote.ErrExists) {
		t.Fatalf("mkdir twice: %v", err)
	}
	if err := rc.Upload(ctx, id, "Up", "a.bin", bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatal(err)
	}
	if n, err := rc.FileSize(ctx, id, "Up/a.bin"); err != nil || n != int64(len(data)) {
		t.Fatalf("FileSize = %d, %v", n, err)
	}
	part, err := rc.ReadRange(ctx, id, "Up/a.bin", 100_000, 5000)
	if err != nil || !bytes.Equal(part, data[100_000:105_000]) {
		t.Fatalf("ReadRange: %v", err)
	}
	tail, err := rc.ReadRange(ctx, id, "Up/a.bin", int64(len(data))-10, 100)
	if err != nil || !bytes.Equal(tail, data[len(data)-10:]) {
		t.Fatalf("tail: %d bytes, %v", len(tail), err)
	}
	rd, total, err := rc.Open(ctx, id, "Up/a.bin", 0, -1)
	if err != nil || total != int64(len(data)) {
		t.Fatalf("Open: %d %v", total, err)
	}
	all, _ := io.ReadAll(rd)
	rd.Close()
	if !bytes.Equal(all, data) {
		t.Fatal("full read differs")
	}
	if err := rc.Move(ctx, id, "Up/a.bin", "Up/b.bin"); err != nil {
		t.Fatal(err)
	}
	if _, err := rc.FileSize(ctx, id, "Up/a.bin"); !errors.Is(err, remote.ErrNotFound) {
		t.Fatalf("old name: %v", err)
	}
	if th, err := rc.Thumbnail(ctx, id, "Up/b.bin", 200); err != nil || th != nil {
		t.Fatalf("thumbnail of a non-image = %d bytes, %v", len(th), err)
	}
	for _, e := range entries {
		if !e.IsDir && strings.HasSuffix(strings.ToLower(e.Name), ".png") {
			if th, err := rc.Thumbnail(ctx, id, e.Name, 128); err != nil || len(th) == 0 {
				t.Fatalf("thumbnail of %s: %v", e.Name, err)
			}
		}
	}
	if err := rc.Delete(ctx, id, "Up"); err != nil {
		t.Fatal(err)
	}

	// Lock, then the client unlocks again by itself only if it still knows
	// the password: Lock forgets it.
	if err := rc.Lock(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := rc.List(ctx, id, ""); !errors.Is(err, remote.ErrLocked) {
		t.Fatalf("after lock: %v", err)
	}
	if err := rc.Unlock(ctx, id, fixturePassword); err != nil {
		t.Fatal(err)
	}
	srv.sessions.closeAll()
	if _, err := rc.List(ctx, id, ""); err != nil {
		t.Fatalf("after the server forgot the session: %v", err)
	}
	if err := rc.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := rc.List(ctx, id, ""); !errors.Is(err, remote.ErrLocked) {
		t.Fatalf("after logout: %v", err)
	}
}

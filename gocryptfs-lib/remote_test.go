//go:build shimtest

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/adrianm/imagethat/webserver/server"
)

// TestOpenRemoteAgainstRealServer runs the real imagethat-web server in
// process over a copy of the fixture vault and opens it through
// GocryptfsOpenRemote: the session must read exactly what the local session
// reads, including thumbnails, and refuse writes.
func TestOpenRemoteAgainstRealServer(t *testing.T) {
	if _, err := os.Stat(filepath.Join(fixtureDir, "gocryptfs.conf")); err != nil {
		t.Skip("fixture vault missing")
	}
	root := t.TempDir()
	if err := os.CopyFS(filepath.Join(root, "v"), os.DirFS(fixtureDir)); err != nil {
		t.Fatal(err)
	}
	srv, err := server.New(server.Config{Root: root, Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/vaults")
	if err != nil {
		t.Fatal(err)
	}
	var vs struct {
		Vaults []struct{ ID string } `json:"vaults"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&vs)
	resp.Body.Close()
	if len(vs.Vaults) != 1 {
		t.Fatalf("server found %d vaults", len(vs.Vaults))
	}
	id := vs.Vaults[0].ID

	if rc := tOpenRemote(ts.URL, id, "wrong", ""); rc != rcWrongPassword {
		t.Errorf("wrong password = %d, want %d", rc, rcWrongPassword)
	}
	if rc := tOpenRemote(ts.URL, "nope", fixturePassword, ""); rc != rcNoConfig {
		t.Errorf("unknown vault = %d, want %d", rc, rcNoConfig)
	}
	if rc := tOpenRemote("not a url", id, fixturePassword, ""); rc != rcCrypto {
		t.Errorf("bad URL = %d, want %d", rc, rcCrypto)
	}

	rid := tOpenRemote(ts.URL, id, fixturePassword, "")
	if rid <= 0 {
		t.Fatalf("GocryptfsOpenRemote = %d", rid)
	}
	defer tClose(rid)
	lid := tOpen(filepath.Join(root, "v"), fixturePassword)
	if lid <= 0 {
		t.Fatal(lid)
	}
	defer tClose(lid)

	rl, err1 := tList(rid, "")
	ll, err2 := tList(lid, "")
	if err1 != nil || err2 != nil || len(rl) != len(ll) || len(rl) == 0 {
		t.Fatalf("listing: remote %d (%v), local %d (%v)", len(rl), err1, len(ll), err2)
	}
	for _, e := range ll {
		if e.IsDir {
			continue
		}
		rb, errR := tDecryptFile(rid, e.Name)
		lb, errL := tDecryptFile(lid, e.Name)
		if errR != nil || errL != nil || !bytes.Equal(rb, lb) {
			t.Errorf("%s: remote %d bytes (%v), local %d (%v)", e.Name, len(rb), errR, len(lb), errL)
		}
		if chunk, n := tReadRange(rid, e.Name, 0, 4097); n >= 0 && !bytes.Equal(chunk, lb[:min(len(lb), 4097)]) {
			t.Errorf("%s: ReadRange differs over the remote session", e.Name)
		}
	}
	if jpg, err := tThumbGet(rid, "photo.jpg", 64); err != nil || len(jpg) < 2 || jpg[0] != 0xFF {
		t.Errorf("thumbnail over a remote session: %v (%d bytes)", err, len(jpg))
	}
	if rc := tMkdir(rid, "new"); rc != rcNoConfig {
		t.Errorf("Mkdir on a remote session = %d, want %d (read-only)", rc, rcNoConfig)
	}
}

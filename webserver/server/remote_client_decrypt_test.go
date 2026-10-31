package server

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/adrianm/imagethat/core/remote"
	"github.com/adrianm/imagethat/core/vault"
)

// The Go client (used by the Mac and Android apps) against a client-decrypt
// server: log in with the access password, unlock locally, read ciphertext.
func TestRemoteClientDecrypt(t *testing.T) {
	srv, root := newClientDecryptServer(t)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	ctx := context.Background()

	rc, err := remote.New(ts.URL, remote.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if mode, err := rc.Info(ctx); err != nil || mode != remote.ModeClientDecrypt {
		t.Fatalf("Info = %q, %v", mode, err)
	}
	if err := rc.Login(ctx, "wrong"); !errors.Is(err, remote.ErrWrongAccessPassword) {
		t.Fatalf("wrong access password: %v", err)
	}

	// Find the vault ID through an HTTP session that has logged in.
	hc := newClient(t, srv)
	hc.login(testAccessPassword)
	id := hc.vaultID()

	if _, err := rc.OpenVault(ctx, id, fixturePassword); err == nil {
		t.Fatal("opened a vault before logging in")
	}
	if err := rc.Login(ctx, testAccessPassword); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if _, err := rc.OpenVault(ctx, id, "not the password"); !errors.Is(err, remote.ErrWrongPassword) {
		t.Errorf("wrong vault password: %v", err)
	}
	if _, err := rc.OpenVault(ctx, "nope", fixturePassword); !errors.Is(err, remote.ErrNotFound) {
		t.Errorf("unknown vault: %v", err)
	}

	rs, err := rc.OpenVault(ctx, id, fixturePassword)
	if err != nil {
		t.Fatalf("OpenVault: %v", err)
	}
	defer rs.Close()
	local, err := vault.OpenLocal(filepath.Join(root, "myvault"), fixturePassword)
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()

	want := walk(t, local, "")
	got := walk(t, rs, "")
	if len(got) != len(want) || len(want) == 0 {
		t.Fatalf("remote lists %d files, local %d", len(got), len(want))
	}
	for i, p := range want {
		if got[i] != p {
			t.Fatalf("file %d: remote %q, local %q", i, got[i], p)
		}
		a, err1 := rs.ReadFile(p)
		b, err2 := local.ReadFile(p)
		if err1 != nil || err2 != nil || !bytes.Equal(a, b) {
			t.Fatalf("%s differs (%v, %v)", p, err1, err2)
		}
	}

	// The vault password never reached the server: it holds no vault open.
	for _, b := range srv.sessions.sessions {
		if len(b.unlockedIDs()) != 0 {
			t.Error("a vault was unlocked on the server")
		}
	}

	// The server's idle reaper (or a restart) drops every session; the client
	// logs in again with the access password it holds.
	srv.sessions.closeAll()
	a, err := rs.ReadFile("photo.jpg")
	b, _ := local.ReadFile("photo.jpg")
	if err != nil || !bytes.Equal(a, b) {
		t.Fatalf("read after the server dropped the session: %v", err)
	}
}

// The Go client cannot open a vault on a server-decrypt server, and never
// sends it the vault password: the server holds nothing unlocked afterwards.
func TestRemoteServerDecryptRefused(t *testing.T) {
	srv, _ := newTestServer(t)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	id := newClient(t, srv).vaultID()
	ctx := context.Background()

	rc, err := remote.New(ts.URL, remote.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if mode, err := rc.Info(ctx); err != nil || mode != remote.ModeServerDecrypt {
		t.Fatalf("Info = %q, %v", mode, err)
	}
	if err := rc.Login(ctx, "anything"); !errors.Is(err, remote.ErrNotClientDecrypt) {
		t.Errorf("Login on a server-decrypt server: %v", err)
	}
	if _, err := rc.OpenVault(ctx, id, fixturePassword); !errors.Is(err, remote.ErrLoginRequired) {
		t.Errorf("OpenVault on a server-decrypt server: %v, want ErrLoginRequired", err)
	}
	for _, b := range srv.sessions.sessions {
		if len(b.unlockedIDs()) != 0 {
			t.Error("a vault was unlocked on the server")
		}
	}
}

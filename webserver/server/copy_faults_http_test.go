package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"path/filepath"
	"testing"
	"time"

	"github.com/adrianm/imagethat/core/faulttest"
	"github.com/adrianm/imagethat/core/vault"
	"github.com/adrianm/imagethat/core/xfer"
)

// Server-decrypt mode, where the server holds the key and the apps send it
// plaintext. The Go client does not speak this mode, so these checks use
// plain HTTP through the fault proxy, as the browser and the Mac app's
// server-decrypt code do.

// decryptSide is a server-decrypt server behind a proxy, unlocked by an HTTP
// client that goes through the proxy.
type decryptSide struct {
	root  string
	id    string
	proxy *faulttest.Proxy
	hc    *http.Client
}

func newDecryptSide(t *testing.T, prepare func(vaultDir string)) *decryptSide {
	t.Helper()
	srv, root := newTestServer(t)
	if prepare != nil {
		prepare(filepath.Join(root, "myvault"))
	}
	ts := serve(t, srv)
	t.Cleanup(ts.Close)
	ds := &decryptSide{root: root, id: newClient(t, srv).vaultID()}
	ds.proxy = faulttest.NewProxy(t, ts.Listener.Addr().String())
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	ds.hc = &http.Client{Jar: jar, Timeout: 10 * time.Second}

	body, _ := json.Marshal(unlockRequest{Password: fixturePassword})
	resp, err := ds.hc.Post(ds.url("unlock", ""), "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unlock: HTTP %d", resp.StatusCode)
	}
	return ds
}

func (ds *decryptSide) url(op, query string) string {
	return fmt.Sprintf("%s/api/vaults/%s/%s?%s", ds.proxy.URL(), ds.id, op, query)
}

func (ds *decryptSide) upload(name string, data []byte) error {
	resp, err := ds.hc.Post(ds.url("upload", "path=&name="+name), "application/octet-stream", bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("upload: HTTP %d", resp.StatusCode)
	}
	return nil
}

func (ds *decryptSide) readLocal(name string) ([]byte, error) {
	s, err := vault.OpenLocal(filepath.Join(ds.root, "myvault"), fixturePassword)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	return s.ReadFile(name)
}

// An upload cut halfway must leave nothing in the vault, and the same upload
// sent again must land whole.
func TestServerDecryptUploadCutLeavesNothing(t *testing.T) {
	ds := newDecryptSide(t, nil)
	want := payload(faultSize)

	ds.proxy.Arm(faulttest.Fault{Dir: faulttest.Up, At: faultSize / 2, Action: faulttest.Reset})
	if err := ds.upload("cut.bin", want); err == nil {
		t.Fatal("an upload cut halfway reported success")
	}
	if !ds.proxy.Fired() {
		t.Fatal("the fault never fired")
	}
	r := &rig{
		got:       func() ([]byte, error) { return ds.readLocal("cut.bin") },
		leftovers: func() []string { return tempsUnder(ds.root) },
	}
	r.assertNothing(t)

	if err := ds.upload("cut.bin", want); err != nil {
		t.Fatalf("the upload sent again: %v", err)
	}
	r.want = want
	r.assertCopied(t)
}

// A download cut partway can be finished with a range request from the
// byte it stopped at, which is what the engine's resume relies on.
func TestServerDecryptDownloadResumes(t *testing.T) {
	want := payload(faultSize)
	ds := newDecryptSide(t, func(dir string) {
		s, err := vault.OpenLocal(dir, fixturePassword)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if _, err := s.CopyIn(context.Background(), "big.bin", xfer.OpenFile(writeSource(t, want)), xfer.Options{}); err != nil {
			t.Fatal(err)
		}
	})

	ds.proxy.Arm(faulttest.Truncate(faultSize / 2))
	resp, err := ds.hc.Get(ds.url("file", "path=big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !ds.proxy.Fired() || err == nil || len(first) >= faultSize {
		t.Fatalf("the download was not cut: %d bytes, err %v, fired %v", len(first), err, ds.proxy.Fired())
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Logf("cut download ended with %v", err)
	}

	req, _ := http.NewRequest(http.MethodGet, ds.url("file", "path=big.bin"), nil)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-", len(first)))
	resp, err = ds.hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("range request: HTTP %d, want 206", resp.StatusCode)
	}
	rest, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(append(first, rest...)) != sha256.Sum256(want) {
		t.Errorf("first %d + rest %d bytes differ from the %d-byte file", len(first), len(rest), len(want))
	}
}

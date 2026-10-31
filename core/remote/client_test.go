package remote

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"
)

// TestOpenNeedsLogin: without Login, opening a vault fails before anything
// is sent, so no vault password can reach the server.
func TestOpenNeedsLogin(t *testing.T) {
	fake := &fakeServer{dir: t.TempDir()}
	ts := httptest.NewServer(fake)
	defer ts.Close()
	c, err := New(ts.URL, Options{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.OpenVault(context.Background(), "v1", testPassword); !errors.Is(err, ErrLoginRequired) || !errors.Is(err, ErrLocked) {
		t.Errorf("OpenVault without login = %v, want ErrLoginRequired", err)
	}
	if _, err := c.OpenVaultCached(context.Background(), "v1", testPassword, nil); !errors.Is(err, ErrLoginRequired) {
		t.Errorf("OpenVaultCached without login = %v, want ErrLoginRequired", err)
	}
	if n := fake.allReqs.Load(); n != 0 {
		t.Errorf("%d requests reached the server", n)
	}
}

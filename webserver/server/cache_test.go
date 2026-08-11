package server

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// TestBlobCacheConcurrent hammers one key from many goroutines.
//
// Get used to read the entry's sealed bytes after releasing the mutex, while
// Put rewrote that field under it. Run with -race, this is the regression
// test for that.
func TestBlobCacheConcurrent(t *testing.T) {
	c, err := newBlobCache(4 << 20)
	if err != nil {
		t.Fatalf("newBlobCache: %v", err)
	}
	defer c.Close()

	payloads := make([][]byte, 8)
	for i := range payloads {
		payloads[i] = bytes.Repeat([]byte{byte('a' + i)}, 1024*(i+1))
	}

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				// Every goroutine contends on the same few keys, so reads and
				// in-place updates overlap.
				key := fmt.Sprintf("key-%d", j%3)
				if j%2 == 0 {
					c.Put(key, payloads[(i+j)%len(payloads)])
					continue
				}
				got, ok := c.Get(key)
				if !ok {
					continue
				}
				// Whatever comes back must be one of the payloads intact, not
				// a torn mixture of two.
				var valid bool
				for _, p := range payloads {
					if bytes.Equal(got, p) {
						valid = true
						break
					}
				}
				if !valid {
					t.Errorf("cache returned %d bytes matching no payload", len(got))
					return
				}
			}
		}(i)
	}
	wg.Wait()
}

// TestBlobCacheBudgetOnUpdate covers replacing entries with larger ones.
//
// Put's update branch used to return before the eviction loop, so the cache
// could grow past its budget without bound.
func TestBlobCacheBudgetOnUpdate(t *testing.T) {
	const budget = 8 << 10
	c, err := newBlobCache(budget)
	if err != nil {
		t.Fatalf("newBlobCache: %v", err)
	}
	defer c.Close()

	for i := 0; i < 4; i++ {
		c.Put(fmt.Sprintf("k%d", i), bytes.Repeat([]byte("x"), 512))
	}
	// Now grow each of them repeatedly in place.
	for round := 0; round < 20; round++ {
		for i := 0; i < 4; i++ {
			c.Put(fmt.Sprintf("k%d", i), bytes.Repeat([]byte("y"), 1500))
		}
	}

	c.mu.Lock()
	cost, max := c.cost, c.maxCost
	c.mu.Unlock()
	if cost > max {
		t.Errorf("cache cost %d exceeds budget %d after in-place growth", cost, max)
	}
}

// TestListVaultsCreatesNoSession checks that an unauthenticated request cannot
// allocate server-side state. Minting a session per cookie-less request let
// anyone who could reach the port grow the session map without bound.
func TestListVaultsCreatesNoSession(t *testing.T) {
	srv, _ := newTestServer(t)

	for i := 0; i < 25; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/vaults", nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		for _, ck := range rec.Result().Cookies() {
			if ck.Name == sessionCookieName && ck.Value != "" {
				t.Fatalf("GET /api/vaults set a session cookie")
			}
		}
	}

	srv.sessions.mu.Lock()
	n := len(srv.sessions.sessions)
	srv.sessions.mu.Unlock()
	if n != 0 {
		t.Errorf("%d sessions created by unauthenticated requests, want 0", n)
	}
}

// TestUnlockStillCreatesSession is the other half: /unlock must issue a cookie.
func TestUnlockStillCreatesSession(t *testing.T) {
	srv, _ := newTestServer(t)
	c := newClient(t, srv)
	id := c.vaultID()

	if c.cookie != nil {
		t.Fatal("a cookie was issued before unlocking")
	}
	c.unlock(id)
	if c.cookie == nil {
		t.Fatal("unlock did not issue a session cookie")
	}

	srv.sessions.mu.Lock()
	n := len(srv.sessions.sessions)
	srv.sessions.mu.Unlock()
	if n != 1 {
		t.Errorf("session count = %d, want 1", n)
	}
}

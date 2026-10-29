package cache

import (
	"bytes"
	"fmt"
	"sync"
	"testing"
)

func TestBlobCacheRoundTripAndEviction(t *testing.T) {
	c, err := NewBlob(1024)
	if err != nil {
		t.Fatalf("NewBlob: %v", err)
	}
	payload := []byte("some sensitive preview bytes")
	c.Put("a", payload)

	got, ok := c.Get("a")
	if !ok || !bytes.Equal(got, payload) {
		t.Fatalf("Get(a) = %q, %v; want the original payload", got, ok)
	}

	// Filling past the budget must evict, not grow without bound.
	for i := 0; i < 200; i++ {
		c.Put(string(rune('a'+i%26))+string(rune('0'+i/26)), bytes.Repeat([]byte("x"), 100))
	}
	if c.cost > c.maxCost {
		t.Errorf("cache cost %d exceeds budget %d", c.cost, c.maxCost)
	}

	// An oversized entry is skipped rather than flushing everything.
	c.Put("huge", bytes.Repeat([]byte("y"), 5000))
	if _, ok := c.Get("huge"); ok {
		t.Error("oversized entry was cached")
	}

	c.Close()
	if _, ok := c.Get("a"); ok {
		t.Error("cache returned an entry after Close")
	}
}

// TestBlobCacheConcurrent hammers one key from many goroutines.
//
// Get used to read the entry's sealed bytes after releasing the mutex, while
// Put rewrote that field under it. Run with -race, this is the regression
// test for that.
func TestBlobCacheConcurrent(t *testing.T) {
	c, err := NewBlob(4 << 20)
	if err != nil {
		t.Fatalf("NewBlob: %v", err)
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
	c, err := NewBlob(budget)
	if err != nil {
		t.Fatalf("NewBlob: %v", err)
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

package server

import (
	"sync"
	"time"
)

// rateLimiter is a fixed-window counter used to slow down unlock attempts.
//
// gocryptfs' scrypt parameters already make guessing expensive, but a limiter
// keeps a scripted attacker from pinning every core on the machine while they
// try.
type rateLimiter struct {
	limit  int
	window time.Duration

	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	count int
	reset time.Time
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{
		limit:   limit,
		window:  window,
		buckets: make(map[string]*bucket),
	}
}

// allow records an attempt against key and reports whether it may proceed.
func (rl *rateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	b, ok := rl.buckets[key]
	if !ok || now.After(b.reset) {
		rl.buckets[key] = &bucket{count: 1, reset: now.Add(rl.window)}
		rl.sweep(now)
		return true
	}
	if b.count >= rl.limit {
		return false
	}
	b.count++
	return true
}

// reset clears a key's counter, called after a successful unlock so a user who
// mistyped a few times is not left throttled.
func (rl *rateLimiter) reset(key string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	delete(rl.buckets, key)
}

// sweep drops expired buckets so the map cannot grow without bound.
func (rl *rateLimiter) sweep(now time.Time) {
	if len(rl.buckets) < 1024 {
		return
	}
	for k, b := range rl.buckets {
		if now.After(b.reset) {
			delete(rl.buckets, k)
		}
	}
}

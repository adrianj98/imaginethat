package server

import (
	"container/list"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
)

// crypter seals cache entries with AES-256-GCM under a key that exists only
// for the lifetime of one unlocked vault.
//
// Cached thumbnails are derived from vault content, so they are just as
// sensitive as the originals. Keeping them encrypted means a core dump, a
// swapped-out page or a heap inspection yields ciphertext, and wiping the key
// when the vault locks makes every remaining entry unreadable immediately.
type crypter struct {
	mu   sync.RWMutex
	aead cipher.AEAD
	key  []byte
}

func newCrypter() (*crypter, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate cache key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &crypter{aead: aead, key: key}, nil
}

var errCrypterWiped = errors.New("cache key has been wiped")

// seal encrypts plaintext, returning nonce||ciphertext.
func (c *crypter) seal(plaintext []byte) ([]byte, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.aead == nil {
		return nil, errCrypterWiped
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return c.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// open decrypts a sealed entry.
func (c *crypter) open(sealed []byte) ([]byte, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.aead == nil {
		return nil, errCrypterWiped
	}
	ns := c.aead.NonceSize()
	if len(sealed) < ns {
		return nil, errors.New("cache entry truncated")
	}
	return c.aead.Open(nil, sealed[:ns], sealed[ns:], nil)
}

// wipe destroys the key. Entries sealed with it can never be read again.
func (c *crypter) wipe() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.key {
		c.key[i] = 0
	}
	c.key = nil
	c.aead = nil
}

// blobCache is a byte-budgeted LRU of encrypted blobs, held in memory only.
//
// Nothing here is ever written to disk: the project's rule is that decrypted
// vault content stays in memory, and an encrypted-at-rest in-memory cache is
// the strictest reading of that.
type blobCache struct {
	crypt   *crypter
	maxCost int64

	mu      sync.Mutex
	cost    int64
	entries map[string]*list.Element
	order   *list.List // front = most recently used
}

type blobEntry struct {
	key    string
	sealed []byte
}

func newBlobCache(maxCost int64) (*blobCache, error) {
	c, err := newCrypter()
	if err != nil {
		return nil, err
	}
	return &blobCache{
		crypt:   c,
		maxCost: maxCost,
		entries: make(map[string]*list.Element),
		order:   list.New(),
	}, nil
}

// Get returns the decrypted value for a key.
func (c *blobCache) Get(key string) ([]byte, bool) {
	c.mu.Lock()
	el, ok := c.entries[key]
	// Read the sealed bytes under the lock: Put replaces this field in place
	// when it refreshes an existing key, so reading it afterwards would race a
	// concurrent request for the same key.
	var sealed []byte
	if ok {
		c.order.MoveToFront(el)
		sealed = el.Value.(*blobEntry).sealed
	}
	c.mu.Unlock()
	if !ok {
		return nil, false
	}

	plain, err := c.crypt.open(sealed)
	if err != nil {
		// The key was wiped or the entry is corrupt; drop it.
		c.remove(key)
		return nil, false
	}
	return plain, true
}

// Put stores a value, encrypting it and evicting until the budget fits.
func (c *blobCache) Put(key string, value []byte) {
	if int64(len(value)) > c.maxCost {
		return // never worth evicting everything else for one entry
	}
	sealed, err := c.crypt.seal(value)
	if err != nil {
		return // vault locked mid-request; skip caching
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.entries[key]; ok {
		e := el.Value.(*blobEntry)
		c.cost -= int64(len(e.sealed))
		// Replace the slice header rather than writing into the old backing
		// array — Get may still be decrypting it outside the lock.
		e.sealed = sealed
		c.cost += int64(len(sealed))
		c.order.MoveToFront(el)
		// Falls through to eviction: replacing entries with larger ones would
		// otherwise grow the cache past its budget indefinitely.
	} else {
		el := c.order.PushFront(&blobEntry{key: key, sealed: sealed})
		c.entries[key] = el
		c.cost += int64(len(sealed))
	}

	for c.cost > c.maxCost {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		e := oldest.Value.(*blobEntry)
		c.order.Remove(oldest)
		delete(c.entries, e.key)
		c.cost -= int64(len(e.sealed))
	}
}

func (c *blobCache) remove(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[key]
	if !ok {
		return
	}
	e := el.Value.(*blobEntry)
	c.order.Remove(el)
	delete(c.entries, key)
	c.cost -= int64(len(e.sealed))
}

// Close wipes the cache key and drops every entry.
func (c *blobCache) Close() {
	c.mu.Lock()
	c.entries = make(map[string]*list.Element)
	c.order.Init()
	c.cost = 0
	c.mu.Unlock()
	c.crypt.wipe()
}

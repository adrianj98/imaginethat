package mobile

import "github.com/adrianm/imagethat/core/cache"

// The network cache is the one read-through cache every network transport
// (Drive, imagethat-web servers) keeps what it reads in, as ciphertext. Pass
// the same directory to OpenDrive, Server.OpenVault and OpenRemoteCached so
// they share one store and one size limit.

// networkRanges is the store in dir, or nil (no caching) for "".
func networkRanges(dir string) (*cache.Ranges, error) {
	if dir == "" {
		return nil, nil
	}
	r, err := cache.SharedRanges(dir, 0)
	return r, wrapErr(err)
}

// SetNetworkCacheLimit sets the network cache's size limit in bytes and
// evicts down to it. Files kept with CacheFile are never evicted.
func SetNetworkCacheLimit(dir string, bytes int64) error {
	r, err := cache.SharedRanges(dir, bytes)
	if err != nil {
		return wrapErr(err)
	}
	r.SetBudget(bytes)
	return nil
}

// NetworkCacheSize is the bytes held in the network cache.
func NetworkCacheSize(dir string) int64 {
	r, err := cache.SharedRanges(dir, 0)
	if err != nil {
		return 0
	}
	return r.Size()
}

// ClearNetworkCache empties the network cache, kept files included.
func ClearNetworkCache(dir string) error {
	r, err := cache.SharedRanges(dir, 0)
	if err != nil {
		return wrapErr(err)
	}
	return wrapErr(r.Clear())
}

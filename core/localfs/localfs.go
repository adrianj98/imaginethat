// Package localfs opens vaults in local folders. A folder on a local disk is
// read directly; a folder on a network mount (SMB, NFS, AFP, WebDAV) is a
// network transport like any other, so its reads go through the cache layer
// (cachefs) and land in the shared network cache.
package localfs

import (
	"os"
	"path/filepath"
	"strconv"

	"github.com/adrianm/imagethat/core/cache"
	"github.com/adrianm/imagethat/core/cachefs"
	"github.com/adrianm/imagethat/core/vault"
)

// Open unlocks the vault at dir. ranges is the shared network cache; with it,
// a vault on a network mount is read through the cache layer. On a local disk,
// or with ranges nil, it is vault.OpenLocal.
func Open(dir, password string, ranges *cache.Ranges) (*vault.Session, error) {
	if ranges == nil || !isNetwork(dir) {
		return vault.OpenLocal(dir, password)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	src := &source{LocalFS: vault.NewLocalFS(abs), root: abs}
	return vault.Open(cachefs.NewWrite(src, ranges, "file\x00"+abs+"\x00"), password)
}

// isNetwork is IsNetworkMount; tests swap it to exercise the cached path
// without a real SMB share.
var isNetwork = IsNetworkMount

// source is a local folder as a cachefs.Source.
type source struct {
	*vault.LocalFS
	root string
}

// Stat versions a file by its modification time and size, which every write
// changes. One stat per open, not per read: on a network mount each is a
// round trip.
func (s *source) Stat(path string) (string, int64, error) {
	fi, err := os.Stat(filepath.Join(s.root, path))
	if err != nil {
		return "", 0, err
	}
	return strconv.FormatInt(fi.ModTime().UnixNano(), 10) + "/" + strconv.FormatInt(fi.Size(), 10), fi.Size(), nil
}

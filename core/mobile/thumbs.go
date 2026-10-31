package mobile

import (
	"context"
	"errors"

	"github.com/adrianm/imagethat/core/cache"
	"github.com/adrianm/imagethat/core/thumb"
)

// ConfigureThumbnails sets up this vault's preview caches: an encrypted
// memory tier of memBytes and, when diskDir is not empty, a disk tier there of
// at most diskBytes, sealed with the vault's own key so no decrypted preview
// is ever stored. sourceID must be stable across launches (the vault's URI or
// server and ID). Zero sizes pick the defaults.
func (v *Vault) ConfigureThumbnails(sourceID, diskDir string, memBytes, diskBytes int64) error {
	opts := thumb.Options{MemBytes: memBytes}
	if diskDir != "" {
		opts.Disk = cache.SharedThumbDisk(diskDir, diskBytes)
		opts.Sealer = v.s
	}
	svc, err := thumb.New(sourceID, thumb.SessionReader{S: v.s}, opts)
	if err != nil {
		return wrapErr(err)
	}
	v.mu.Lock()
	if v.thumbs != nil {
		v.thumbs.Close()
	}
	v.thumbs = svc
	v.mu.Unlock()
	return nil
}

func (v *Vault) thumbService() (*thumb.Service, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.thumbs == nil {
		svc, err := thumb.New("", thumb.SessionReader{S: v.s}, thumb.Options{})
		if err != nil {
			return nil, err
		}
		v.thumbs = svc
	}
	return v.thumbs, nil
}

func (v *Vault) invalidateThumb(path string) {
	v.mu.Lock()
	svc := v.thumbs
	v.mu.Unlock()
	if svc != nil {
		svc.Invalidate(path)
	}
}

// Thumbnail returns a JPEG preview at most maxEdge pixels on its long edge,
// from the caches or decoded here. It returns an empty array when Go cannot
// decode the file (HEIC, RAW, video): render it on the device and hand it to
// StoreThumbnail, so the caches learn it.
func (v *Vault) Thumbnail(path string, maxEdge int) ([]byte, error) {
	svc, err := v.thumbService()
	if err != nil {
		return nil, wrapErr(err)
	}
	jpeg, err := svc.Get(context.Background(), path, maxEdge)
	if errors.Is(err, thumb.ErrNoPreview) {
		return []byte{}, nil
	}
	if err != nil {
		return nil, wrapErr(err)
	}
	return append([]byte(nil), jpeg...), nil
}

// StoreThumbnail caches a preview the device rendered itself.
func (v *Vault) StoreThumbnail(path string, maxEdge int, jpeg []byte) error {
	svc, err := v.thumbService()
	if err != nil {
		return wrapErr(err)
	}
	return wrapErr(svc.Put(path, maxEdge, jpeg))
}

// InvalidateThumbnail drops every cached preview of path.
func (v *Vault) InvalidateThumbnail(path string) { v.invalidateThumb(path) }

// ThumbCacheSize is the size of a thumbnail disk cache directory.
func ThumbCacheSize(dir string) int64 { return cache.SharedThumbDisk(dir, 0).Size() }

// ClearThumbCache empties a thumbnail disk cache directory.
func ClearThumbCache(dir string) error { return wrapErr(cache.SharedThumbDisk(dir, 0).Clear()) }

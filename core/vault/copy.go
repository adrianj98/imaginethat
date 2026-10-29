package vault

import (
	"context"
	"fmt"
	"io"

	"github.com/adrianm/imagethat/core/xfer"
)

// PermanentErrors are the vault errors a copy must not retry.
var PermanentErrors = []error{
	ErrNoConfig, ErrWrongPassword, ErrCrypto, ErrNotFound, ErrClosed,
	ErrReadOnlyBackend, ErrExists, ErrInvalidPath, ErrPermission,
}

// CopyIn copies the file that open yields into the vault at plainPath,
// encrypting as it streams. Reads that fail with a transient error are
// retried and resume where they stopped (see package xfer). The ciphertext
// goes to a temp file and is renamed into place only once every byte has
// arrived, so a copy that fails or is cancelled leaves nothing at plainPath.
//
// Returns the number of plaintext bytes written.
func (s *Session) CopyIn(ctx context.Context, plainPath string, open xfer.Opener, opts xfer.Options) (int64, error) {
	opts.Permanent = append(append([]error(nil), opts.Permanent...), PermanentErrors...)
	r := xfer.NewReader(ctx, open, opts)
	defer r.Close()
	size, err := r.Size()
	if err != nil {
		return 0, fmt.Errorf("open source: %w", err)
	}
	written, err := s.Create(plainPath, r)
	if err != nil {
		return written, err
	}
	// encryptStream ends at the first EOF; make sure that was the real end.
	if written != size {
		_ = s.Remove(plainPath)
		return written, fmt.Errorf("copy to %s: wrote %d of %d bytes: %w", plainPath, written, size, xfer.ErrTruncated)
	}
	return written, nil
}

// Opener returns an xfer.Opener over a plaintext file in this vault, for
// copying it somewhere else.
func (s *Session) Opener(plainPath string) xfer.Opener {
	return func() (xfer.Source, error) { return s.Open(plainPath) }
}

// RawOpener returns an xfer.Opener over one ciphertext file of a VFS.
func RawOpener(v VFS, cipherPath string) xfer.Opener {
	return func() (xfer.Source, error) {
		size, err := v.FileSize(cipherPath)
		if err != nil {
			return nil, err
		}
		return rawSource{v: v, path: cipherPath, size: size}, nil
	}
}

type rawSource struct {
	v    VFS
	path string
	size int64
}

func (r rawSource) ReadAt(p []byte, off int64) (int, error) { return r.v.ReadAt(r.path, off, p) }
func (r rawSource) Size() int64                             { return r.size }
func (r rawSource) Close() error                            { return nil }

// Cacher is an optional VFS capability for backends that can keep a local
// copy of remote ciphertext. Paths are ciphertext paths.
type Cacher interface {
	Cache(ctx context.Context, cipherPath string, opts xfer.Options) error
	IsCached(cipherPath string) bool
	Uncache(cipherPath string) error
}

// CacheFile makes a file readable without the network. A backend that keeps
// its own local copies (Cacher) fetches the ciphertext into them. Any other
// backend has the file's ciphertext read through once, which fills whatever
// cache the backend keeps behind its reads (the macOS app's Drive caches do
// this). Either way the reads retry and resume like a copy, and nothing
// decrypted is stored.
func (s *Session) CacheFile(ctx context.Context, plainPath string, opts xfer.Options) error {
	cipherPath, err := s.CipherPath(plainPath)
	if err != nil {
		return err
	}
	if cipherPath == "" {
		return fmt.Errorf("%w: is a directory", ErrInvalidPath)
	}
	opts.Permanent = append(append([]error(nil), opts.Permanent...), PermanentErrors...)
	if c, ok := s.fs.(Cacher); ok {
		return c.Cache(ctx, cipherPath, opts)
	}
	r := xfer.NewReader(ctx, RawOpener(s.fs, cipherPath), opts)
	defer r.Close()
	_, err = io.Copy(io.Discard, r)
	return err
}

// IsCached reports whether a Cacher backend holds a local copy of the file.
// It is always false for other backends; they report their own caches.
func (s *Session) IsCached(plainPath string) bool {
	c, ok := s.fs.(Cacher)
	if !ok {
		return false
	}
	cipherPath, err := s.CipherPath(plainPath)
	return err == nil && cipherPath != "" && c.IsCached(cipherPath)
}

// Uncache drops a Cacher backend's local copy of the file, if there is one.
func (s *Session) Uncache(plainPath string) error {
	c, ok := s.fs.(Cacher)
	if !ok {
		return nil
	}
	cipherPath, err := s.CipherPath(plainPath)
	if err != nil {
		return err
	}
	return c.Uncache(cipherPath)
}

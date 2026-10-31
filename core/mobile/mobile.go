// Package mobile is a gomobile-bindable wrapper over the vault package for
// local (on-device) gocryptfs vaults. Only types gomobile supports appear in
// the exported API. Errors carry a stable code prefix, see wrapErr.
package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/adrianm/imagethat/core/drive"
	"github.com/adrianm/imagethat/core/errcode"
	"github.com/adrianm/imagethat/core/remote"
	"github.com/adrianm/imagethat/core/thumb"
	"github.com/adrianm/imagethat/core/vault"
)

// wrapErr prefixes err with a stable code Kotlin can switch on
// (errcode.Code: wrong_password:, not_found:, exists:, closed:, locked:, ...).
// Errors without a code are returned unchanged.
func wrapErr(err error) error {
	if c := errcode.Code(err); c != "" {
		return fmt.Errorf("%s: %w", c, err)
	}
	return err
}

// SetTempDir sets the directory Go uses for temporary files (TMPDIR). Android
// does not hand the process environment to Go, so os.TempDir falls back to
// /data/local/tmp, which apps cannot write. Call it before InitLocal.
func SetTempDir(dir string) error {
	return os.Setenv("TMPDIR", dir)
}

// InitLocal creates a new empty vault in dir.
func InitLocal(dir, password string) error {
	return wrapErr(vault.Init(vault.NewLocalFS(dir), password))
}

// Vault is an unlocked vault: local, in a SAF folder, on a server or in
// Google Drive.
type Vault struct {
	s *vault.Session
	// drive is set for a vault in Google Drive.
	drive *drive.VaultFS

	mu     sync.Mutex
	thumbs *thumb.Service // set up by ConfigureThumbnails or on first use
}

// OpenLocal unlocks the vault in dir.
func OpenLocal(dir, password string) (*Vault, error) {
	s, err := vault.OpenLocal(dir, password)
	if err != nil {
		return nil, wrapErr(err)
	}
	return &Vault{s: s}, nil
}

// OpenRemote opens a vault on an imagethat-web server in ciphertext mode: the
// server sends the vault's encrypted files and decryption happens here.
// pinnedSHA256 is the hex SHA-256 of a self-signed server's certificate, or
// "". accessPassword is the server access password: only client-decrypt
// servers (see ServerMode) are supported, and the vault password never leaves
// the device. Without one nothing is sent and the error is "locked:". The
// vault is read-only. Errors carry the usual codes plus "cert_mismatch:" and
// "wrong_access_password:".
func OpenRemote(baseURL, vaultID, password, pinnedSHA256, accessPassword string) (*Vault, error) {
	c, err := remote.New(baseURL, remote.Options{PinnedSHA256: pinnedSHA256})
	if err != nil {
		return nil, wrapErr(err)
	}
	ctx := context.Background()
	if accessPassword != "" {
		if err := c.Login(ctx, accessPassword); err != nil {
			return nil, wrapErr(err)
		}
	}
	s, err := c.OpenVault(ctx, vaultID, password)
	if err != nil {
		return nil, wrapErr(err)
	}
	return &Vault{s: s}, nil
}

// ServerMode reports how an imagethat-web server works: "server-decrypt"
// (this engine cannot open its vaults) or "client-decrypt" (log in with its
// access password and decrypt here).
func ServerMode(baseURL, pinnedSHA256 string) (string, error) {
	c, err := remote.New(baseURL, remote.Options{PinnedSHA256: pinnedSHA256})
	if err != nil {
		return "", wrapErr(err)
	}
	mode, err := c.Info(context.Background())
	if err != nil {
		return "", wrapErr(err)
	}
	return mode, nil
}

// List returns the directory's entries as a JSON array of vault.Entry.
func (v *Vault) List(path string) (string, error) {
	entries, err := v.s.ListDir(path)
	if err != nil {
		return "", wrapErr(err)
	}
	if entries == nil {
		entries = []vault.Entry{}
	}
	b, err := json.Marshal(entries)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Size returns the plaintext size of a file.
func (v *Vault) Size(path string) (int64, error) {
	f, err := v.s.Open(path)
	if err != nil {
		return 0, wrapErr(err)
	}
	defer f.Close()
	return f.Size(), nil
}

// ReadRange reads up to n bytes at off. Reading past the end returns a short
// (possibly empty) slice and no error.
func (v *Vault) ReadRange(path string, off int64, n int) ([]byte, error) {
	if n < 0 || off < 0 {
		return nil, wrapErr(fmt.Errorf("%w: negative offset or length", vault.ErrInvalidPath))
	}
	f, err := v.s.Open(path)
	if err != nil {
		return nil, wrapErr(err)
	}
	defer f.Close()
	buf := make([]byte, n)
	got, err := f.ReadAt(buf, off)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, wrapErr(err)
	}
	return buf[:got], nil
}

// Create starts writing a new file; the entry appears on FileWriter.Close.
func (v *Vault) Create(path string) (*FileWriter, error) {
	return newFileWriter(v.s, path, func() { v.invalidateThumb(path) }), nil
}

// Mkdir creates a directory.
func (v *Vault) Mkdir(path string) error { return wrapErr(v.s.Mkdir(path)) }

// Move renames a file or directory.
func (v *Vault) Move(from, to string) error {
	err := v.s.Move(from, to)
	if err == nil {
		v.invalidateThumb(from)
	}
	return wrapErr(err)
}

// Remove deletes a file or directory.
func (v *Vault) Remove(path string) error {
	v.invalidateThumb(path)
	return wrapErr(v.s.Remove(path))
}

// Close wipes the vault's keys and its in-memory previews.
func (v *Vault) Close() error {
	v.mu.Lock()
	if v.thumbs != nil {
		v.thumbs.Close()
		v.thumbs = nil
	}
	v.mu.Unlock()
	return wrapErr(v.s.Close())
}

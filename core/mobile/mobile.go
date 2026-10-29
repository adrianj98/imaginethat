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
	"io/fs"
	"os"

	"github.com/adrianm/imagethat/core/remote"
	"github.com/adrianm/imagethat/core/vault"
)

// wrapErr prefixes err with a stable code Kotlin can switch on:
// wrong_password:, no_config:, not_found:, exists:, invalid_path:, closed:.
// Errors without a code are returned unchanged.
func wrapErr(err error) error {
	if err == nil {
		return nil
	}
	var code string
	switch {
	case errors.Is(err, vault.ErrWrongPassword):
		code = "wrong_password"
	case errors.Is(err, vault.ErrNoConfig):
		code = "no_config"
	case errors.Is(err, vault.ErrNotFound), errors.Is(err, fs.ErrNotExist):
		code = "not_found"
	case errors.Is(err, vault.ErrExists), errors.Is(err, fs.ErrExist):
		code = "exists"
	case errors.Is(err, vault.ErrInvalidPath):
		code = "invalid_path"
	case errors.Is(err, vault.ErrClosed):
		code = "closed"
	default:
		return err
	}
	return fmt.Errorf("%s: %w", code, err)
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

// Vault is an unlocked local vault.
type Vault struct {
	s *vault.Session
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
// "". accessPassword is the server access password of a client-decrypt
// server (see ServerMode), after which the vault password never leaves the
// device; pass "" for a server-decrypt server. The vault is read-only; writes
// go through the server's API. Errors carry the usual codes plus
// "cert_mismatch:" and "wrong_access_password:".
func OpenRemote(baseURL, vaultID, password, pinnedSHA256, accessPassword string) (*Vault, error) {
	c, err := remote.New(baseURL, remote.Options{PinnedSHA256: pinnedSHA256})
	if err != nil {
		return nil, wrapErr(err)
	}
	ctx := context.Background()
	if accessPassword != "" {
		if err := c.Login(ctx, accessPassword); err != nil {
			return nil, wrapRemoteErr(err)
		}
	}
	s, err := c.OpenVault(ctx, vaultID, password)
	if err != nil {
		return nil, wrapRemoteErr(err)
	}
	return &Vault{s: s}, nil
}

// ServerMode reports how an imagethat-web server works: "server-decrypt"
// (send it the vault password) or "client-decrypt" (log in with its access
// password and decrypt here).
func ServerMode(baseURL, pinnedSHA256 string) (string, error) {
	c, err := remote.New(baseURL, remote.Options{PinnedSHA256: pinnedSHA256})
	if err != nil {
		return "", wrapErr(err)
	}
	mode, err := c.Info(context.Background())
	if err != nil {
		return "", wrapRemoteErr(err)
	}
	return mode, nil
}

func wrapRemoteErr(err error) error {
	switch {
	case errors.Is(err, remote.ErrWrongAccessPassword):
		return fmt.Errorf("wrong_access_password: %w", err)
	case errors.Is(err, remote.ErrWrongPassword):
		return fmt.Errorf("wrong_password: %w", err)
	case errors.Is(err, remote.ErrNotFound):
		return fmt.Errorf("not_found: %w", err)
	case errors.Is(err, remote.ErrCertMismatch):
		return fmt.Errorf("cert_mismatch: %w", err)
	}
	return wrapErr(err)
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
	return newFileWriter(v.s, path), nil
}

// Mkdir creates a directory.
func (v *Vault) Mkdir(path string) error { return wrapErr(v.s.Mkdir(path)) }

// Move renames a file or directory.
func (v *Vault) Move(from, to string) error { return wrapErr(v.s.Move(from, to)) }

// Remove deletes a file or directory.
func (v *Vault) Remove(path string) error { return wrapErr(v.s.Remove(path)) }

// Close wipes the vault's keys.
func (v *Vault) Close() error { return wrapErr(v.s.Close()) }

package mobile

import (
	"context"
	"io"

	"github.com/adrianm/imagethat/core/errcode"
	"github.com/adrianm/imagethat/core/remote"
	"github.com/adrianm/imagethat/core/vault"
)

// Server is a connection to one imagethat-web server: its session cookie,
// the pinned certificate, and the passwords it re-authenticates with after
// the server's idle timeout, all in memory. It speaks both modes: the
// server-decrypt API (List, ReadRange, Upload, ...) and, for client-decrypt
// servers, OpenVault, which decrypts here.
type Server struct {
	c *remote.Client
}

// NewServer returns a client for the server at baseURL. pinnedSHA256 is the
// hex SHA-256 of a self-signed server's certificate, or "".
func NewServer(baseURL, pinnedSHA256 string) (*Server, error) {
	c, err := remote.New(baseURL, remote.Options{PinnedSHA256: pinnedSHA256})
	if err != nil {
		return nil, wrapErr(err)
	}
	return &Server{c: c}, nil
}

// Mode is "server-decrypt" or "client-decrypt".
func (s *Server) Mode() (string, error) {
	m, err := s.c.Info(context.Background())
	return m, wrapErr(err)
}

// Login sends a client-decrypt server its access password.
func (s *Server) Login(accessPassword string) error {
	return wrapErr(s.c.Login(context.Background(), accessPassword))
}

// Vaults lists the server's vaults as a JSON array of {id, name, unlocked}.
func (s *Server) Vaults() (string, error) {
	vs, err := s.c.Vaults(context.Background())
	if err != nil {
		return "", wrapErr(err)
	}
	if vs == nil {
		vs = []remote.VaultInfo{}
	}
	return jsonString(vs)
}

// Unlock unlocks a vault on a server-decrypt server.
func (s *Server) Unlock(vaultID, password string) error {
	return wrapErr(s.c.Unlock(context.Background(), vaultID, password))
}

// Lock locks a vault on the server.
func (s *Server) Lock(vaultID string) error {
	return wrapErr(s.c.Lock(context.Background(), vaultID))
}

// Logout ends the server session and forgets every password.
func (s *Server) Logout() error {
	return wrapErr(s.c.Logout(context.Background()))
}

// List returns a decrypted directory as a JSON array of
// {name, isDir, size, mediaType}, the same shape as Vault.List.
func (s *Server) List(vaultID, path string) (string, error) {
	entries, err := s.c.List(context.Background(), vaultID, path)
	if err != nil {
		return "", wrapErr(err)
	}
	if entries == nil {
		entries = []vault.Entry{}
	}
	return jsonString(entries)
}

// Size is a file's decrypted size.
func (s *Server) Size(vaultID, path string) (int64, error) {
	n, err := s.c.FileSize(context.Background(), vaultID, path)
	return n, wrapErr(err)
}

// ReadRange reads up to n bytes at off; fewer at the end of the file.
func (s *Server) ReadRange(vaultID, path string, off int64, n int) ([]byte, error) {
	b, err := s.c.ReadRange(context.Background(), vaultID, path, off, n)
	return b, wrapErr(err)
}

// Thumbnail is the server's JPEG preview, or an empty array when it has none.
func (s *Server) Thumbnail(vaultID, path string, size int) ([]byte, error) {
	b, err := s.c.Thumbnail(context.Background(), vaultID, path, size)
	if err != nil {
		return nil, wrapErr(err)
	}
	if b == nil {
		b = []byte{}
	}
	return b, nil
}

// Upload starts streaming a new file dir/name to the server, which encrypts
// it as it arrives. size is the byte count, or -1 if unknown. Write the data,
// then Close to finish or Abort to cancel.
func (s *Server) Upload(vaultID, dir, name string, size int64) (*FileWriter, error) {
	// Abort fails the pipe, which fails the request body and ends the upload.
	return newPipeWriter(func(r io.Reader) error {
		return s.c.Upload(context.Background(), vaultID, dir, name, r, size)
	}), nil
}

// Mkdir creates a directory.
func (s *Server) Mkdir(vaultID, path string) error {
	return wrapErr(s.c.Mkdir(context.Background(), vaultID, path))
}

// Move renames or moves a file or directory.
func (s *Server) Move(vaultID, from, to string) error {
	return wrapErr(s.c.Move(context.Background(), vaultID, from, to))
}

// Delete removes a file or directory.
func (s *Server) Delete(vaultID, path string) error {
	return wrapErr(s.c.Delete(context.Background(), vaultID, path))
}

// OpenVault opens a vault over the server's ciphertext endpoints and decrypts
// it here (client-decrypt servers, after Login). cacheDir, when not empty,
// keeps files passed to CacheFile. Read-only.
func (s *Server) OpenVault(vaultID, password, cacheDir string) (*Vault, error) {
	var sess *vault.Session
	var err error
	if cacheDir != "" {
		sess, err = s.c.OpenVaultCached(context.Background(), vaultID, password, cacheDir)
	} else {
		sess, err = s.c.OpenVault(context.Background(), vaultID, password)
	}
	if err != nil {
		return nil, wrapErr(err)
	}
	return &Vault{s: sess}, nil
}

// errcodeNotFound reports whether err means "no such thing".
func errcodeNotFound(err error) bool { return errcode.Code(err) == errcode.NotFound }

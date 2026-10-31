package mobile

import (
	"context"

	"github.com/adrianm/imagethat/core/errcode"
	"github.com/adrianm/imagethat/core/remote"
	"github.com/adrianm/imagethat/core/vault"
)

// Server is a connection to one imagethat-web server: its session cookie,
// the pinned certificate and the access password it logs in again with after
// the server's idle timeout, all in memory. The server only ever sends
// ciphertext; OpenVault decrypts here.
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

// Vaults lists the server's vaults as a JSON array of {id, name}.
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

// Logout ends the server session and forgets every password.
func (s *Server) Logout() error {
	return wrapErr(s.c.Logout(context.Background()))
}

// OpenVault opens a vault over the server's ciphertext endpoints and decrypts
// it here (client-decrypt servers, after Login; "locked:" before it). cacheDir, when not empty,
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

// Package errcode gives engine errors the short, stable codes both apps
// switch on. The Android binding prefixes error messages with them
// ("not_found: ...") and the macOS shim puts them in error strings the same
// way, so the two apps tell failures apart identically.
package errcode

import (
	"context"
	"errors"
	"io/fs"

	"github.com/adrianm/imagethat/core/drive"
	"github.com/adrianm/imagethat/core/remote"
	"github.com/adrianm/imagethat/core/vault"
)

// Codes, in the order they are checked.
const (
	WrongAccessPassword = "wrong_access_password"
	WrongPassword       = "wrong_password"
	CertMismatch        = "cert_mismatch"
	NoConfig            = "no_config"
	NotFound            = "not_found"
	Exists              = "exists"
	InvalidPath         = "invalid_path"
	Closed              = "closed"
	Locked              = "locked"
	ReadOnly            = "read_only"
	Forbidden           = "forbidden"
	Cancelled           = "cancelled"
	NotConnected        = "not_connected"
	Auth                = "auth"
)

// Code returns err's code, or "" for an error without one.
func Code(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, remote.ErrWrongAccessPassword):
		return WrongAccessPassword
	case errors.Is(err, vault.ErrWrongPassword), errors.Is(err, remote.ErrWrongPassword):
		return WrongPassword
	case errors.Is(err, remote.ErrCertMismatch):
		return CertMismatch
	case errors.Is(err, vault.ErrNoConfig):
		return NoConfig
	case errors.Is(err, vault.ErrNotFound), errors.Is(err, remote.ErrNotFound), errors.Is(err, fs.ErrNotExist):
		return NotFound
	case errors.Is(err, vault.ErrExists), errors.Is(err, remote.ErrExists), errors.Is(err, fs.ErrExist):
		return Exists
	case errors.Is(err, vault.ErrInvalidPath):
		return InvalidPath
	case errors.Is(err, vault.ErrClosed):
		return Closed
	case errors.Is(err, remote.ErrLocked):
		return Locked
	case errors.Is(err, vault.ErrReadOnlyBackend):
		return ReadOnly
	case errors.Is(err, remote.ErrForbidden), errors.Is(err, vault.ErrPermission), errors.Is(err, fs.ErrPermission):
		return Forbidden
	case errors.Is(err, context.Canceled), errors.Is(err, drive.ErrCancelled):
		return Cancelled
	case errors.Is(err, drive.ErrNotConnected):
		return NotConnected
	case errors.Is(err, drive.ErrAuth):
		return Auth
	}
	return ""
}

// Message is err's text with its code in front ("not_found: ..."), or the
// plain text when it has no code.
func Message(err error) string {
	if c := Code(err); c != "" {
		return c + ": " + err.Error()
	}
	return err.Error()
}

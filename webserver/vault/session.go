// Package vault provides read and write access to a gocryptfs vault as a Go
// library.
//
// It is a port of the crypto logic in gocryptfs-lib/main.go, which exposes the
// same capability only through cgo //export symbols for the macOS app. The
// logic here is deliberately kept recognisable against that file so the two
// can be compared: openSession, resolveCipherPath, readDirIV and the block
// arithmetic all have direct counterparts there.
//
// Plaintext never touches disk. Decrypted bytes live in caller-owned buffers,
// and plaintext scratch buffers are zeroed once copied.
package vault

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/adrianm/imagethat/webserver/internal/gocryptfs/configfile"
	"github.com/adrianm/imagethat/webserver/internal/gocryptfs/contentenc"
	"github.com/adrianm/imagethat/webserver/internal/gocryptfs/cryptocore"
	"github.com/adrianm/imagethat/webserver/internal/gocryptfs/nametransform"
)

// Errors returned by this package. Callers distinguish them with errors.Is.
var (
	// ErrNoConfig means the directory has no gocryptfs.conf and is not a vault.
	ErrNoConfig = errors.New("vault: gocryptfs.conf not found")
	// ErrWrongPassword means the master key could not be unwrapped.
	ErrWrongPassword = errors.New("vault: wrong password")
	// ErrCrypto means the config parsed but the cipher could not be set up.
	ErrCrypto = errors.New("vault: unsupported or broken crypto configuration")
	// ErrNotFound means no such plaintext path exists in the vault.
	ErrNotFound = errors.New("vault: no such file or directory")
	// ErrClosed means the session has been closed and its keys wiped.
	ErrClosed = errors.New("vault: session is closed")
	// ErrReadOnlyBackend means the storage backend cannot be written to.
	ErrReadOnlyBackend = errors.New("vault: backend is read-only")
	// ErrExists means the destination path is already taken.
	ErrExists = errors.New("vault: destination already exists")
	// ErrInvalidPath means the path escapes the vault or is otherwise illegal.
	ErrInvalidPath = errors.New("vault: invalid path")
)

// Session is an unlocked vault. It is safe for concurrent use.
type Session struct {
	fs VFS

	cryptoCore    *cryptocore.CryptoCore
	contentEnc    *contentenc.ContentEnc
	nameTransform *nametransform.NameTransform

	plaintextNames bool
	longNames      bool
	useDirIV       bool

	mu     sync.RWMutex
	closed bool
	// dirIV caches per-directory IVs, keyed by ciphertext directory path.
	// A directory's IV is fixed for its lifetime, so this is safe; write
	// operations that create or destroy directories invalidate it.
	dirIV map[string][]byte
}

// IsVault reports whether dir looks like a gocryptfs vault.
func IsVault(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, configfile.ConfDefaultName))
	return err == nil
}

// OpenLocal unlocks the gocryptfs vault rooted at vaultPath.
//
// On success the master key has already been wiped from memory; only the
// derived cipher state is retained.
func OpenLocal(vaultPath, password string) (*Session, error) {
	confPath := filepath.Join(vaultPath, configfile.ConfDefaultName)
	if _, err := os.Stat(confPath); err != nil {
		return nil, ErrNoConfig
	}
	cf, err := configfile.Load(confPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoConfig, err)
	}
	return newSession(cf, password, NewLocalFS(vaultPath))
}

// newSession mirrors openSession() in gocryptfs-lib/main.go.
func newSession(cf *configfile.ConfFile, password string, fs VFS) (*Session, error) {
	masterkey, err := cf.DecryptMasterKey([]byte(password))
	if err != nil {
		return nil, ErrWrongPassword
	}
	// From here on every exit path must wipe the master key.
	defer wipe(masterkey)

	algo, err := cf.ContentEncryption()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCrypto, err)
	}

	useHKDF := cf.IsFeatureFlagSet(configfile.FlagHKDF)
	cc := cryptocore.New(masterkey, algo, contentenc.DefaultIVBits, useHKDF)
	ce := contentenc.New(cc, contentenc.DefaultBS)

	plaintextNames := cf.IsFeatureFlagSet(configfile.FlagPlaintextNames)
	longNames := cf.IsFeatureFlagSet(configfile.FlagLongNames)
	raw64 := cf.IsFeatureFlagSet(configfile.FlagRaw64)
	useDirIV := cf.IsFeatureFlagSet(configfile.FlagDirIV)

	var nt *nametransform.NameTransform
	if !plaintextNames {
		// deterministicNames is the inverse of the DirIV flag: without
		// per-directory IVs, names encrypt the same everywhere.
		nt = nametransform.New(cc.EMECipher, longNames, cf.LongNameMax, raw64, nil, !useDirIV)
	}

	return &Session{
		fs:             fs,
		cryptoCore:     cc,
		contentEnc:     ce,
		nameTransform:  nt,
		plaintextNames: plaintextNames,
		longNames:      longNames,
		useDirIV:       useDirIV,
		dirIV:          make(map[string][]byte),
	}, nil
}

// Close wipes the session's key material. Further calls return ErrClosed.
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.dirIV = nil
	if s.contentEnc != nil {
		s.contentEnc.Wipe()
	}
	if s.cryptoCore != nil {
		s.cryptoCore.Wipe()
	}
	return nil
}

// checkOpen returns ErrClosed once the session has been closed.
func (s *Session) checkOpen() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return ErrClosed
	}
	return nil
}

// writeFS returns the backend's write interface, mirroring the localFS() guard
// in gocryptfs-lib/main.go that makes write operations local-only.
func (s *Session) writeFS() (WriteVFS, error) {
	w, ok := s.fs.(WriteVFS)
	if !ok {
		return nil, ErrReadOnlyBackend
	}
	return w, nil
}

// PlainBS is the plaintext block size (4096 bytes).
func (s *Session) PlainBS() uint64 { return s.contentEnc.PlainBS() }

// wipe zeroes a key buffer.
func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

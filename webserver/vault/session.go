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
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
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
	// ErrPermission means the backend denied access to the ciphertext — the
	// vault exists but the server's OS user cannot read it.
	ErrPermission = errors.New("vault: permission denied on the backend")
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

	// Logf, if set, receives diagnostics that would otherwise be swallowed —
	// currently only undecryptable directory entries, which are the reason a
	// folder can render as unexpectedly empty.
	//
	// It is opt-in and nil by default because the messages name plaintext
	// vault paths. Emitting those to a default logger is exactly what makes
	// gocryptfs-lib's unconditional /tmp/gocryptfs.log a liability; whether
	// they are worth writing down is the embedder's call, not this package's.
	Logf func(format string, args ...any)

	mu     sync.RWMutex
	closed bool
	// dirIV caches per-directory IVs, keyed by ciphertext directory path.
	// A directory's IV is fixed for its lifetime, so this is safe; write
	// operations that create or destroy directories invalidate it, and a name
	// that fails to decrypt drops the entry in case another process recreated
	// the directory underneath us.
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
		// A conf we cannot even stat because of permissions is a different
		// problem than a directory that simply is not a vault, and reporting
		// it as "not a vault" sends whoever is debugging in the wrong
		// direction entirely.
		if errors.Is(err, fs.ErrPermission) {
			return nil, fmt.Errorf("%w: %v", ErrPermission, err)
		}
		return nil, ErrNoConfig
	}
	data, err := os.ReadFile(confPath)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return nil, fmt.Errorf("%w: %v", ErrPermission, err)
		}
		return nil, fmt.Errorf("%w: %v", ErrNoConfig, err)
	}
	cf, err := parseConfig(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoConfig, err)
	}
	return newSession(cf, password, NewLocalFS(vaultPath))
}

// Open unlocks a vault whose ciphertext lives behind any VFS, such as a
// storage backend that is not a local directory. Writes through the returned
// session work only if v also implements WriteVFS.
func Open(v VFS, password string) (*Session, error) {
	data, err := v.ReadFile(configfile.ConfDefaultName)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNoConfig
		}
		if errors.Is(err, fs.ErrPermission) {
			return nil, fmt.Errorf("%w: %v", ErrPermission, err)
		}
		return nil, fmt.Errorf("%w: %v", ErrNoConfig, err)
	}
	cf, err := parseConfig(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoConfig, err)
	}
	return newSession(cf, password, v)
}

// parseConfig is configfile.Load for a gocryptfs.conf already in memory; it
// mirrors parseConfig in gocryptfs-lib/main.go.
func parseConfig(data []byte) (*configfile.ConfFile, error) {
	if len(data) == 0 {
		return nil, errors.New("config file is empty")
	}
	var cf configfile.ConfFile
	if err := json.Unmarshal(data, &cf); err != nil {
		return nil, err
	}
	if err := cf.Validate(); err != nil {
		return nil, err
	}
	return &cf, nil
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
	// The IV length is a property of the cipher, not a constant: AES-GCM and
	// AES-SIV use 128-bit IVs but XChaCha20-Poly1305 needs 192-bit ones, and
	// cryptocore.New panics on a mismatch — so passing DefaultIVBits
	// unconditionally makes an XChaCha vault impossible to open at all.
	// gocryptfs-lib/main.go has the same bug.
	//
	// Deriving it from the backend is exact here because configfile.Validate
	// rejects every other combination: AES-GCM and AES-SIV both require the
	// GCMIV128 flag, and XChaCha conflicts with it.
	ivBits := algo.NonceSize * 8

	cc := cryptocore.New(masterkey, algo, ivBits, useHKDF)
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
	// ContentEnc.Wipe() wipes the CryptoCore it holds and then drops its
	// reference to it, so this must not be followed by a second
	// cryptoCore.Wipe(): for the AES-SIV and OpenSSL backends that second call
	// type-asserts an already-nil AEADCipher and panics.
	switch {
	case s.contentEnc != nil:
		s.contentEnc.Wipe()
	case s.cryptoCore != nil:
		s.cryptoCore.Wipe()
	}
	return nil
}

// decryptBlock decrypts one ciphertext block under the session lock.
//
// The lock matters: Close() wipes the ContentEnc's CryptoCore reference, and an
// in-flight read that had already passed checkOpen() would otherwise dereference
// a nil pointer inside DecryptBlock. Taking the read lock here makes locking a
// vault mid-stream return ErrClosed instead of panicking.
func (s *Session) decryptBlock(ciphertext []byte, blockNo uint64, fileID []byte) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, ErrClosed
	}
	return s.contentEnc.DecryptBlock(ciphertext, blockNo, fileID)
}

// encryptBlock is the write-side counterpart of decryptBlock.
func (s *Session) encryptBlock(plaintext []byte, blockNo uint64, fileID []byte) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, ErrClosed
	}
	return s.contentEnc.EncryptBlock(plaintext, blockNo, fileID), nil
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

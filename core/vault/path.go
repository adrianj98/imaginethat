package vault

import (
	"fmt"
	"path"
	"strings"

	"github.com/adrianm/imagethat/core/internal/gocryptfs/nametransform"
)

// dirIVLen is the length of a gocryptfs.diriv file.
const dirIVLen = 16

// CleanPath normalises a caller-supplied plaintext path to a vault-relative
// slash path with no leading slash, no "." and no "..". The root is "".
//
// Every path entering the package goes through this, so a traversal attempt
// from an HTTP query parameter cannot reach outside the vault.
//
// A ".." component is rejected rather than resolved. Anchoring it to the root
// would also be safe, but silently acting on a different path than the caller
// named is the wrong contract for an API — a malformed request should fail
// loudly, and no legitimate client sends "..".
func CleanPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	p = strings.ReplaceAll(p, "\\", "/")

	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return "", fmt.Errorf("%w: %q contains %q", ErrInvalidPath, p, "..")
		}
		if strings.ContainsRune(part, 0) {
			return "", fmt.Errorf("%w: %q contains a NUL byte", ErrInvalidPath, p)
		}
	}

	// path.Clean collapses "//", "." and any trailing slash. Anchoring to "/"
	// first keeps a leading-slash path from being treated as absolute.
	cleaned := strings.TrimPrefix(path.Clean("/"+p), "/")
	if cleaned == "." {
		cleaned = ""
	}
	// Defence in depth: nothing above can produce these, but the whole
	// package's safety rests on this function.
	for _, part := range splitPath(cleaned) {
		if part == ".." || part == "." || part == "" {
			return "", fmt.Errorf("%w: %q", ErrInvalidPath, p)
		}
	}
	return cleaned, nil
}

// splitPath splits a cleaned vault-relative path into its components.
func splitPath(p string) []string {
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// readDirIV returns the IV of a ciphertext directory, caching it.
//
// A directory's IV is written once at creation and never changes, so caching
// is safe; operations that create or remove directories drop the cache.
func (s *Session) readDirIV(cipherDir string) ([]byte, error) {
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return nil, ErrClosed
	}
	if iv, ok := s.dirIV[cipherDir]; ok {
		s.mu.RUnlock()
		return iv, nil
	}
	s.mu.RUnlock()

	ivPath := path.Join(cipherDir, nametransform.DirIVFilename)
	iv, err := s.fs.ReadFile(ivPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", ivPath, err)
	}
	if len(iv) != dirIVLen {
		return nil, fmt.Errorf("%s: expected %d bytes, got %d", ivPath, dirIVLen, len(iv))
	}

	s.mu.Lock()
	if !s.closed {
		s.dirIV[cipherDir] = iv
	}
	s.mu.Unlock()
	return iv, nil
}

// forgetDirIVs drops the whole IV cache. Called after operations that change
// the set of directories.
func (s *Session) forgetDirIVs() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dirIV != nil {
		clear(s.dirIV)
	}
}

// forgetDirIV drops one directory's cached IV, so the next read picks up an IV
// written by another process.
func (s *Session) forgetDirIV(cipherDir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dirIV != nil {
		delete(s.dirIV, cipherDir)
	}
}

// encryptName encrypts a single path component inside the given ciphertext
// directory, hashing it to a gocryptfs.longname.* name when it is too long.
func (s *Session) encryptName(cipherDir, plainName string) (string, error) {
	if s.plaintextNames {
		return plainName, nil
	}
	var iv []byte
	if s.useDirIV {
		var err error
		iv, err = s.readDirIV(cipherDir)
		if err != nil {
			return "", err
		}
	}
	cName, err := s.nameTransform.EncryptAndHashName(plainName, iv)
	if err != nil {
		return "", fmt.Errorf("encrypt name %q: %w", plainName, err)
	}
	return cName, nil
}

// resolveCipherPath maps a plaintext vault-relative path to its ciphertext
// path, walking one component at a time because each level's name encryption
// depends on that level's IV.
//
// Unlike its counterpart in gocryptfs-lib/main.go, this returns an error
// instead of silently falling back to the plaintext path — that fallback turns
// an unreadable gocryptfs.diriv into a confusing "file not found" downstream.
func (s *Session) resolveCipherPath(plainRelPath string) (string, error) {
	if err := s.checkOpen(); err != nil {
		return "", err
	}
	parts := splitPath(plainRelPath)
	cipherPath := ""
	for _, part := range parts {
		cName, err := s.encryptName(cipherPath, part)
		if err != nil {
			return "", err
		}
		cipherPath = path.Join(cipherPath, cName)
	}
	return cipherPath, nil
}

// isSpecialFile reports whether a ciphertext entry is gocryptfs bookkeeping
// rather than user content.
func isSpecialFile(name string) bool {
	return name == configFileName ||
		name == nametransform.DirIVFilename ||
		strings.HasSuffix(name, nametransform.LongNameSuffix)
}

const configFileName = "gocryptfs.conf"

// decryptEntryName turns one raw ciphertext directory entry into its plaintext
// name, following the .name sidecar for hashed long names.
func (s *Session) decryptEntryName(cipherDir, cName string, dirIV []byte) (string, error) {
	if s.plaintextNames {
		return cName, nil
	}
	if nametransform.IsLongContent(cName) {
		sidecar := path.Join(cipherDir, cName+nametransform.LongNameSuffix)
		encName, err := s.fs.ReadFile(sidecar)
		if err != nil {
			return "", fmt.Errorf("read long name %s: %w", sidecar, err)
		}
		return s.nameTransform.DecryptName(strings.TrimSpace(string(encName)), dirIV)
	}
	return s.nameTransform.DecryptName(cName, dirIV)
}

// EncryptName returns the ciphertext directory-entry name for plainName inside
// the ciphertext directory cipherDir ("" is the vault root). Long names come
// back in their hashed gocryptfs.longname.* form, which is how they appear on
// disk. Hosts that write ciphertext themselves (a cloud backend that uploads
// files by name) need it; nothing else should.
func (s *Session) EncryptName(cipherDir, plainName string) (string, error) {
	if err := s.checkOpen(); err != nil {
		return "", err
	}
	if plainName == "" || plainName == "." || plainName == ".." ||
		strings.ContainsAny(plainName, "/\\") || strings.ContainsRune(plainName, 0) {
		return "", fmt.Errorf("%w: %q is not a single path component", ErrInvalidPath, plainName)
	}
	return s.encryptName(cipherDir, plainName)
}

// CipherPath maps a plaintext vault-relative path to the ciphertext path of
// the same entry, as stored in the backend. The root maps to "".
func (s *Session) CipherPath(plainPath string) (string, error) {
	clean, err := CleanPath(plainPath)
	if err != nil {
		return "", err
	}
	return s.resolveCipherPath(clean)
}

// LongNameSidecar reports what a backend that writes ciphertext itself must
// store next to an entry named plainName in cipherDir. For a short name it
// returns "" and nothing extra is needed. For a long one, whose on-disk name
// is a gocryptfs.longname.<hash>, it returns the full encrypted name, the
// content of the "<hashed name>.name" sidecar file without which gocryptfs
// cannot list the entry.
func (s *Session) LongNameSidecar(cipherDir, plainName string) (string, error) {
	cName, err := s.EncryptName(cipherDir, plainName)
	if err != nil || s.plaintextNames || !nametransform.IsLongContent(cName) {
		return "", err
	}
	var iv []byte
	if s.useDirIV {
		if iv, err = s.readDirIV(cipherDir); err != nil {
			return "", err
		}
	}
	return s.nameTransform.EncryptName(plainName, iv)
}

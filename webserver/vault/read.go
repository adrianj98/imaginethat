package vault

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
)

// Entry is one decrypted directory entry.
type Entry struct {
	Name  string    `json:"name"`
	IsDir bool      `json:"isDir"`
	Size  int64     `json:"size"` // plaintext size; 0 for directories
	Media MediaType `json:"mediaType"`
}

// ListDir returns the decrypted contents of a plaintext directory path.
// gocryptfs bookkeeping files, dotfiles and macOS AppleDouble entries are
// filtered out, matching VaultSource.listMedia() in the macOS app.
//
// Entries whose names cannot be decrypted are skipped rather than failing the
// whole listing, so one corrupt name does not make a directory unbrowsable.
func (s *Session) ListDir(plainPath string) ([]Entry, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	clean, err := CleanPath(plainPath)
	if err != nil {
		return nil, err
	}
	cipherDir, err := s.resolveCipherPath(clean)
	if err != nil {
		return nil, err
	}

	raw, err := s.fs.ReadDir(cipherDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, plainPath)
		}
		return nil, err
	}

	var dirIV []byte
	if !s.plaintextNames && s.useDirIV {
		if dirIV, err = s.readDirIV(cipherDir); err != nil {
			return nil, err
		}
	}

	out := make([]Entry, 0, len(raw))
	for _, e := range raw {
		if isSpecialFile(e.Name) {
			continue
		}
		name, err := s.decryptEntryName(cipherDir, e.Name, dirIV)
		if err != nil {
			continue
		}
		if isHiddenName(name) {
			continue
		}
		entry := Entry{Name: name, IsDir: e.IsDir}
		if !e.IsDir {
			entry.Size = int64(s.contentEnc.CipherSizeToPlainSize(uint64(e.Size)))
			entry.Media = ClassifyMedia(name)
		}
		out = append(out, entry)
	}

	// Directories first, then case-insensitive by name — the order the app's
	// browser presents.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// isHiddenName filters dotfiles and macOS AppleDouble sidecars, as
// VaultSource.swift:33 does.
func isHiddenName(name string) bool {
	return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "._")
}

// Stat returns metadata for a single plaintext path.
func (s *Session) Stat(plainPath string) (Entry, error) {
	if err := s.checkOpen(); err != nil {
		return Entry{}, err
	}
	clean, err := CleanPath(plainPath)
	if err != nil {
		return Entry{}, err
	}
	if clean == "" {
		return Entry{Name: "", IsDir: true}, nil
	}
	// Look the name up in its parent so we get the directory flag without
	// needing a stat primitive on the VFS.
	parent, name := path.Split(clean)
	parent = strings.TrimSuffix(parent, "/")
	entries, err := s.ListDir(parent)
	if err != nil {
		return Entry{}, err
	}
	for _, e := range entries {
		if e.Name == name {
			return e, nil
		}
	}
	return Entry{}, fmt.Errorf("%w: %s", ErrNotFound, plainPath)
}

// FileSize returns the plaintext size of a file.
func (s *Session) FileSize(plainPath string) (int64, error) {
	if err := s.checkOpen(); err != nil {
		return 0, err
	}
	clean, err := CleanPath(plainPath)
	if err != nil {
		return 0, err
	}
	cipherPath, err := s.resolveCipherPath(clean)
	if err != nil {
		return 0, err
	}
	size, err := s.fs.FileSize(cipherPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, fmt.Errorf("%w: %s", ErrNotFound, plainPath)
		}
		return 0, err
	}
	return int64(s.contentEnc.CipherSizeToPlainSize(uint64(size))), nil
}

// ReadFile decrypts an entire file into memory. Prefer Open for anything
// large — this exists for bounded reads like thumbnail sources.
func (s *Session) ReadFile(plainPath string) ([]byte, error) {
	f, err := s.Open(plainPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, f.Size())
	if _, err := io.ReadFull(f, buf); err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, err
	}
	return buf, nil
}

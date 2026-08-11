package vault

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/adrianm/imagethat/webserver/internal/gocryptfs/contentenc"
	"github.com/adrianm/imagethat/webserver/internal/gocryptfs/cryptocore"
	"github.com/adrianm/imagethat/webserver/internal/gocryptfs/nametransform"
)

// Permissions match what gocryptfs itself writes, so a vault stays consistent
// whether this package or the gocryptfs binary created the file.
const (
	dirIVPerms = 0o444
	namePerms  = 0o444
)

// Create writes a new file into the vault, encrypting r as it streams.
//
// Nothing is buffered whole: plaintext is consumed one 4 KiB block at a time,
// encrypted, and appended to a temporary ciphertext file which is renamed into
// place at the end. That matters for video uploads — the counterpart in
// gocryptfs-lib/main.go takes the entire plaintext as one in-memory buffer.
//
// Returns the number of plaintext bytes written.
func (s *Session) Create(plainPath string, r io.Reader) (int64, error) {
	if err := s.checkOpen(); err != nil {
		return 0, err
	}
	fs, err := s.writeFS()
	if err != nil {
		return 0, err
	}
	clean, err := CleanPath(plainPath)
	if err != nil {
		return 0, err
	}
	if clean == "" {
		return 0, fmt.Errorf("%w: empty path", ErrInvalidPath)
	}

	cipherDir, cName, err := s.resolveParent(clean)
	if err != nil {
		return 0, err
	}
	target := path.Join(cipherDir, cName)
	if fs.Exists(target) {
		return 0, fmt.Errorf("%w: %s", ErrExists, plainPath)
	}

	tmp, tmpPath, err := fs.CreateTemp(cipherDir, ".imagethat-upload-*")
	if err != nil {
		return 0, fmt.Errorf("create temp in %s: %w", cipherDir, err)
	}
	// Until the rename succeeds the temp file is ours to clean up.
	committed := false
	defer func() {
		if !committed {
			tmp.Close()
			_ = fs.Remove(tmpPath)
		}
	}()

	written, err := s.encryptStream(tmp, r)
	if err != nil {
		return 0, err
	}
	if err := tmp.Close(); err != nil {
		return 0, fmt.Errorf("close temp file: %w", err)
	}

	// The long-name sidecar must exist before the entry becomes visible,
	// otherwise a concurrent listing sees a name it cannot decrypt.
	if err := s.writeLongNameSidecar(fs, cipherDir, cName, path.Base(clean)); err != nil {
		return 0, err
	}

	if err := fs.Rename(tmpPath, target); err != nil {
		_ = s.removeLongNameSidecar(fs, cipherDir, cName)
		return 0, fmt.Errorf("commit %s: %w", plainPath, err)
	}
	committed = true
	return written, nil
}

// encryptStream writes the gocryptfs file format — an 18-byte header followed
// by encrypted 4 KiB blocks — consuming r as it goes.
//
// A zero-length file gets no header at all, matching gocryptfs: an empty
// ciphertext file decrypts to empty rather than erroring on a missing header.
func (s *Session) encryptStream(w io.Writer, r io.Reader) (int64, error) {
	plainBS := int(s.contentEnc.PlainBS())
	buf := make([]byte, plainBS)
	defer wipe(buf)

	var (
		header    *contentenc.FileHeader
		blockNo   uint64
		total     int64
		headerOut bool
	)

	for {
		n, readErr := io.ReadFull(r, buf)
		if n > 0 {
			if !headerOut {
				header = contentenc.RandomHeader()
				if _, err := w.Write(header.Pack()); err != nil {
					return total, fmt.Errorf("write header: %w", err)
				}
				headerOut = true
			}
			block := s.contentEnc.EncryptBlock(buf[:n], blockNo, header.ID)
			if _, err := w.Write(block); err != nil {
				return total, fmt.Errorf("write block %d: %w", blockNo, err)
			}
			blockNo++
			total += int64(n)
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
				return total, nil
			}
			return total, fmt.Errorf("read source: %w", readErr)
		}
	}
}

// Mkdir creates a directory in the vault, including its gocryptfs.diriv.
func (s *Session) Mkdir(plainPath string) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	fs, err := s.writeFS()
	if err != nil {
		return err
	}
	clean, err := CleanPath(plainPath)
	if err != nil {
		return err
	}
	if clean == "" {
		return fmt.Errorf("%w: cannot create the vault root", ErrInvalidPath)
	}

	cipherDir, cName, err := s.resolveParent(clean)
	if err != nil {
		return err
	}
	target := path.Join(cipherDir, cName)
	if fs.Exists(target) {
		return fmt.Errorf("%w: %s", ErrExists, plainPath)
	}

	if err := fs.Mkdir(target); err != nil {
		return fmt.Errorf("mkdir %s: %w", plainPath, err)
	}

	// A fresh random IV, so names inside this directory encrypt differently
	// from names anywhere else.
	if s.useDirIV {
		iv := cryptocore.RandBytes(dirIVLen)
		ivPath := path.Join(target, nametransform.DirIVFilename)
		if err := fs.WriteFile(ivPath, iv); err != nil {
			_ = fs.RemoveAll(target)
			return fmt.Errorf("write %s: %w", ivPath, err)
		}
		_ = chmodIfLocal(fs, ivPath, dirIVPerms)
	}

	if err := s.writeLongNameSidecar(fs, cipherDir, cName, path.Base(clean)); err != nil {
		_ = fs.RemoveAll(target)
		return err
	}
	return nil
}

// Remove deletes a file or an empty-or-not directory from the vault.
// Directories are removed recursively, matching the app's delete action.
func (s *Session) Remove(plainPath string) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	fs, err := s.writeFS()
	if err != nil {
		return err
	}
	clean, err := CleanPath(plainPath)
	if err != nil {
		return err
	}
	if clean == "" {
		return fmt.Errorf("%w: refusing to delete the vault root", ErrInvalidPath)
	}

	cipherDir, cName, err := s.resolveParent(clean)
	if err != nil {
		return err
	}
	target := path.Join(cipherDir, cName)
	if !fs.Exists(target) {
		return fmt.Errorf("%w: %s", ErrNotFound, plainPath)
	}

	if err := fs.RemoveAll(target); err != nil {
		return fmt.Errorf("remove %s: %w", plainPath, err)
	}
	// The sidecar is orphaned once the entry is gone.
	_ = s.removeLongNameSidecar(fs, cipherDir, cName)
	s.forgetDirIVs()
	return nil
}

// Move renames or relocates an entry within the vault. The file's contents are
// untouched — gocryptfs content encryption does not depend on the path — but
// the name is re-encrypted under the destination directory's IV.
func (s *Session) Move(fromPath, toPath string) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	fs, err := s.writeFS()
	if err != nil {
		return err
	}
	from, err := CleanPath(fromPath)
	if err != nil {
		return err
	}
	to, err := CleanPath(toPath)
	if err != nil {
		return err
	}
	if from == "" || to == "" {
		return fmt.Errorf("%w: cannot move the vault root", ErrInvalidPath)
	}
	if from == to {
		return nil
	}
	// Moving a directory inside itself would detach the subtree.
	if strings.HasPrefix(to+"/", from+"/") {
		return fmt.Errorf("%w: cannot move %s into itself", ErrInvalidPath, fromPath)
	}

	srcDir, srcName, err := s.resolveParent(from)
	if err != nil {
		return err
	}
	dstDir, dstName, err := s.resolveParent(to)
	if err != nil {
		return err
	}
	srcTarget := path.Join(srcDir, srcName)
	dstTarget := path.Join(dstDir, dstName)

	if !fs.Exists(srcTarget) {
		return fmt.Errorf("%w: %s", ErrNotFound, fromPath)
	}
	if fs.Exists(dstTarget) {
		return fmt.Errorf("%w: %s", ErrExists, toPath)
	}

	if err := s.writeLongNameSidecar(fs, dstDir, dstName, path.Base(to)); err != nil {
		return err
	}
	if err := fs.Rename(srcTarget, dstTarget); err != nil {
		_ = s.removeLongNameSidecar(fs, dstDir, dstName)
		return fmt.Errorf("move %s to %s: %w", fromPath, toPath, err)
	}
	_ = s.removeLongNameSidecar(fs, srcDir, srcName)
	s.forgetDirIVs()
	return nil
}

// resolveParent splits a cleaned plaintext path into its ciphertext parent
// directory and the encrypted form of its final component.
func (s *Session) resolveParent(clean string) (cipherDir, cName string, err error) {
	parent, base := path.Split(clean)
	parent = strings.TrimSuffix(parent, "/")
	cipherDir, err = s.resolveCipherPath(parent)
	if err != nil {
		return "", "", err
	}
	cName, err = s.encryptName(cipherDir, base)
	if err != nil {
		return "", "", err
	}
	return cipherDir, cName, nil
}

// writeLongNameSidecar creates the "<hash>.name" file holding the full
// encrypted name, but only when the name was actually hashed.
func (s *Session) writeLongNameSidecar(fs WriteVFS, cipherDir, cName, plainName string) error {
	if s.plaintextNames || !nametransform.IsLongContent(cName) {
		return nil
	}
	var iv []byte
	if s.useDirIV {
		var err error
		iv, err = s.readDirIV(cipherDir)
		if err != nil {
			return err
		}
	}
	// The sidecar holds the unhashed encrypted name, exactly as
	// nametransform.WriteLongNameAt writes it.
	fullCName, err := s.nameTransform.EncryptName(plainName, iv)
	if err != nil {
		return fmt.Errorf("encrypt long name %q: %w", plainName, err)
	}
	sidecar := path.Join(cipherDir, cName+nametransform.LongNameSuffix)
	if err := fs.WriteFile(sidecar, []byte(fullCName)); err != nil {
		return fmt.Errorf("write %s: %w", sidecar, err)
	}
	_ = chmodIfLocal(fs, sidecar, namePerms)
	return nil
}

// removeLongNameSidecar deletes the "<hash>.name" file if there is one.
func (s *Session) removeLongNameSidecar(fs WriteVFS, cipherDir, cName string) error {
	if s.plaintextNames || !nametransform.IsLongContent(cName) {
		return nil
	}
	return fs.Remove(path.Join(cipherDir, cName+nametransform.LongNameSuffix))
}

// chmodIfLocal applies gocryptfs' read-only permissions to bookkeeping files.
// Backends that are not the local filesystem have no permissions to set.
func chmodIfLocal(fs WriteVFS, relPath string, mode os.FileMode) error {
	local, ok := fs.(*LocalFS)
	if !ok {
		return nil
	}
	return os.Chmod(local.abs(relPath), mode)
}

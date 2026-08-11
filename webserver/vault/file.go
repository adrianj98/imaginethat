package vault

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/adrianm/imagethat/webserver/internal/gocryptfs/contentenc"
)

// File is an open, decrypted view of one file in the vault.
//
// It implements io.ReadSeeker, io.ReaderAt and io.Closer, which is what lets
// the HTTP layer hand it straight to http.ServeContent and get byte ranges,
// 206 responses and If-Range handling for free — the job the macOS app needs a
// bespoke AVAssetResourceLoader for.
//
// Decryption happens per 4 KiB block on demand; the file is never buffered
// whole and no plaintext is written anywhere.
type File struct {
	sess       *Session
	cipherPath string
	plainSize  int64

	// header is parsed once at Open. gocryptfs-lib re-reads and re-parses it
	// on every range request; a video scrub is thousands of those.
	header *contentenc.FileHeader

	// handle is a persistent ciphertext handle when the backend supports one,
	// avoiding an open/close syscall pair per block.
	handle ReaderAtCloser

	mu     sync.Mutex
	pos    int64
	closed bool
}

var (
	_ io.ReadSeeker = (*File)(nil)
	_ io.ReaderAt   = (*File)(nil)
	_ io.Closer     = (*File)(nil)
)

// Open opens a file in the vault for reading.
func (s *Session) Open(plainPath string) (*File, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	clean, err := CleanPath(plainPath)
	if err != nil {
		return nil, err
	}
	if clean == "" {
		return nil, fmt.Errorf("%w: is a directory", ErrInvalidPath)
	}
	cipherPath, err := s.resolveCipherPath(clean)
	if err != nil {
		return nil, err
	}

	cipherSize, err := s.fs.FileSize(cipherPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, plainPath)
		}
		return nil, err
	}

	f := &File{
		sess:       s,
		cipherPath: cipherPath,
		plainSize:  int64(s.contentEnc.CipherSizeToPlainSize(uint64(cipherSize))),
	}

	if opener, ok := s.fs.(FileOpener); ok {
		if h, err := opener.OpenFile(cipherPath); err == nil {
			f.handle = h
		}
	}

	// An empty file has no header at all; everything else must have one.
	if cipherSize > 0 {
		hdrBuf := make([]byte, contentenc.HeaderLen)
		n, err := f.readCipherAt(hdrBuf, 0)
		if err != nil && !errors.Is(err, io.EOF) {
			f.Close()
			return nil, fmt.Errorf("read header of %s: %w", plainPath, err)
		}
		if n < contentenc.HeaderLen {
			f.Close()
			return nil, fmt.Errorf("%s: file too short for header (%d bytes)", plainPath, n)
		}
		header, err := contentenc.ParseHeader(hdrBuf)
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("parse header of %s: %w", plainPath, err)
		}
		f.header = header
	}

	return f, nil
}

// readCipherAt reads raw ciphertext, preferring the persistent handle.
func (f *File) readCipherAt(buf []byte, off int64) (int, error) {
	if f.handle != nil {
		return f.handle.ReadAt(buf, off)
	}
	return f.sess.fs.ReadAt(f.cipherPath, off, buf)
}

// Size returns the plaintext size in bytes.
func (f *File) Size() int64 { return f.plainSize }

// Close releases the underlying ciphertext handle.
func (f *File) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	if f.handle != nil {
		return f.handle.Close()
	}
	return nil
}

// ReadAt implements io.ReaderAt over the plaintext. It is safe for concurrent
// use and does not affect the seek offset.
//
// The block arithmetic mirrors GocryptfsReadRange in gocryptfs-lib/main.go.
func (f *File) ReadAt(p []byte, off int64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if off < 0 {
		return 0, fmt.Errorf("%w: negative offset", ErrInvalidPath)
	}
	if err := f.sess.checkOpen(); err != nil {
		return 0, err
	}
	if off >= f.plainSize {
		return 0, io.EOF
	}

	// Never read past the plaintext end.
	want := int64(len(p))
	if off+want > f.plainSize {
		want = f.plainSize - off
	}

	plainBS := f.sess.contentEnc.PlainBS()
	cipherBS := f.sess.contentEnc.CipherBS()
	firstBlock := uint64(off) / plainBS
	lastBlock := uint64(off+want-1) / plainBS

	blockBuf := make([]byte, cipherBS)
	defer wipe(blockBuf)

	var written int64
	for blockNo := firstBlock; blockNo <= lastBlock; blockNo++ {
		cipherOff := int64(contentenc.HeaderLen) + int64(blockNo*cipherBS)
		n, readErr := f.readCipherAt(blockBuf, cipherOff)
		if n == 0 {
			// Short ciphertext: the file is smaller than its size implied.
			break
		}

		plain, err := f.sess.contentEnc.DecryptBlock(blockBuf[:n], blockNo, f.header.ID)
		if err != nil {
			return int(written), fmt.Errorf("decrypt block %d of %s: %w", blockNo, f.cipherPath, err)
		}

		// Slice the requested window out of this block.
		blockPlainOff := int64(blockNo * plainBS)
		start := int64(0)
		if off > blockPlainOff {
			start = off - blockPlainOff
		}
		end := int64(len(plain))
		if blockPlainOff+end > off+want {
			end = off + want - blockPlainOff
		}
		if start < end {
			written += int64(copy(p[written:], plain[start:end]))
		}
		wipe(plain)

		if readErr != nil {
			// io.EOF here just means the last block was partial.
			break
		}
		if written >= want {
			break
		}
	}

	if written == 0 {
		return 0, io.EOF
	}
	if written < int64(len(p)) {
		// io.ReaderAt requires a non-nil error on a short read.
		return int(written), io.EOF
	}
	return int(written), nil
}

// Read implements io.Reader, advancing the seek offset.
func (f *File) Read(p []byte) (int, error) {
	f.mu.Lock()
	pos := f.pos
	f.mu.Unlock()

	n, err := f.ReadAt(p, pos)
	if n > 0 {
		f.mu.Lock()
		f.pos = pos + int64(n)
		f.mu.Unlock()
		// A short read at the end of the file is not an error for io.Reader;
		// the next Read reports EOF.
		if errors.Is(err, io.EOF) {
			err = nil
		}
	}
	return n, err
}

// Seek implements io.Seeker over the plaintext.
func (f *File) Seek(offset int64, whence int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = f.pos + offset
	case io.SeekEnd:
		abs = f.plainSize + offset
	default:
		return 0, fmt.Errorf("vault: invalid whence %d", whence)
	}
	if abs < 0 {
		return 0, fmt.Errorf("vault: negative seek position %d", abs)
	}
	f.pos = abs
	return abs, nil
}

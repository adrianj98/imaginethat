package vault

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/adrianm/imagethat/core/internal/gocryptfs/contentenc"
)

// maxBlocksPerRead bounds one backend read in ReadAt: 256 blocks is 1 MiB of
// plaintext (about 1.03 MiB of ciphertext), the largest request a player or
// the shims make, so a typical request is a single round trip.
const maxBlocksPerRead = 256

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

	// ahead holds decrypted bytes [aheadOff, aheadOff+len(ahead)) fetched by
	// Read beyond what its caller asked for. Read is the sequential path
	// (http.ServeContent, io.Copy), whose callers pass 32 KiB buffers; without
	// it every 32 KiB is one backend round trip. Memory only, wiped when
	// replaced and on Close. Guarded by mu; ReadAt never touches it.
	ahead    []byte
	aheadOff int64
	// aheadWin is the next refill size: it starts small so a short read (a
	// probe, a header sniff) fetches little, and doubles up to readAheadSize
	// while reads stay sequential.
	aheadWin int
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
	wipe(f.ahead)
	f.ahead = nil
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

	// The ciphertext of consecutive blocks is contiguous, so it is fetched in
	// one backend read per run of up to maxBlocksPerRead blocks rather than one
	// per 4 KiB block. On a network or SAF backend every read is a round trip:
	// per-block reads made a 1 MiB request cost 256 of them.
	runLen := min(lastBlock-firstBlock+1, maxBlocksPerRead)
	cipherBuf := make([]byte, runLen*cipherBS)

	var written int64
	blockNo := firstBlock
	for blockNo <= lastBlock && written < want {
		count := min(lastBlock-blockNo+1, maxBlocksPerRead)
		chunk := cipherBuf[:count*cipherBS]
		cipherOff := int64(contentenc.HeaderLen) + int64(blockNo*cipherBS)
		n, readErr := f.readCipherAt(chunk, cipherOff)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			// A genuine backend failure must not be reported as a clean end of
			// file — that would hand the client a silently truncated download.
			return int(written), fmt.Errorf("read %s: %w", f.cipherPath, readErr)
		}
		if n == 0 {
			// Short ciphertext: the file is smaller than its size implied.
			break
		}

		// Decrypt each block of the run and slice the requested window out.
		for data := chunk[:n]; len(data) > 0 && blockNo <= lastBlock; blockNo++ {
			cblock := data[:min(int(cipherBS), len(data))]
			data = data[len(cblock):]

			plain, err := f.sess.decryptBlock(cblock, blockNo, f.header.ID)
			if err != nil {
				return int(written), fmt.Errorf("decrypt block %d of %s: %w", blockNo, f.cipherPath, err)
			}
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
		}

		if readErr != nil || n < len(chunk) {
			// io.EOF or a short read here means the last block was partial.
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

// readAheadSize is the most Read fetches when its caller asks for less: one
// backend round trip per MiB instead of one per caller buffer. The window
// starts at readAheadMin and grows while reads stay sequential.
const (
	readAheadSize = int(maxBlocksPerRead) * 4096
	readAheadMin  = 128 << 10
)

// Read implements io.Reader, advancing the seek offset. Small reads are served
// from a read-ahead window, so a sequential copy costs one backend read per
// MiB. Like any io.Reader it is not for concurrent use; ReadAt is.
func (f *File) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, ErrClosed
	}
	if f.pos >= f.plainSize {
		return 0, io.EOF
	}

	// Serve from the window when pos falls inside it.
	if f.pos >= f.aheadOff && f.pos < f.aheadOff+int64(len(f.ahead)) {
		n := copy(p, f.ahead[f.pos-f.aheadOff:])
		f.pos += int64(n)
		return n, nil
	}

	// A large read goes straight through: buffering it gains nothing.
	if len(p) >= readAheadSize {
		n, err := f.ReadAt(p, f.pos)
		f.pos += int64(n)
		if n > 0 && errors.Is(err, io.EOF) {
			err = nil // the next Read reports EOF
		}
		return n, err
	}

	// Refill the window at pos, growing it if this continues the last one.
	sequential := len(f.ahead) > 0 && f.pos == f.aheadOff+int64(len(f.ahead))
	switch {
	case !sequential || f.aheadWin == 0:
		f.aheadWin = max(readAheadMin, len(p))
	case f.aheadWin < readAheadSize:
		f.aheadWin = min(f.aheadWin*2, readAheadSize)
	}
	if cap(f.ahead) < f.aheadWin {
		wipe(f.ahead[:cap(f.ahead)])
		f.ahead = make([]byte, f.aheadWin)
	} else {
		wipe(f.ahead[:cap(f.ahead)])
	}
	buf := f.ahead[:f.aheadWin]
	n, err := f.ReadAt(buf, f.pos)
	if n == 0 {
		f.ahead = f.ahead[:0]
		return 0, err
	}
	if err != nil && !errors.Is(err, io.EOF) {
		f.ahead = f.ahead[:0]
		return 0, err
	}
	f.ahead, f.aheadOff = buf[:n], f.pos
	m := copy(p, f.ahead)
	f.pos += int64(m)
	return m, nil
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

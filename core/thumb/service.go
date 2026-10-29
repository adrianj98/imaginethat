package thumb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"sync"

	"github.com/adrianm/imagethat/core/cache"
	"github.com/adrianm/imagethat/core/vault"
)

// ErrNoPreview means the file cannot be previewed: an unsupported format, a
// source too large to decode whole, or a decoder that gave up. Callers show a
// placeholder; it is not a failure of the source.
var ErrNoPreview = errors.New("thumb: no preview available")

// File is an open plaintext file a video decoder can read ranges from.
type File interface {
	io.ReaderAt
	io.Closer
	Size() int64
}

// Reader is what the service needs from a source. SessionReader adapts a
// *vault.Session; a remote or Drive source supplies its own.
type Reader interface {
	FileSize(path string) (int64, error)
	ReadFile(path string) ([]byte, error)
	Open(path string) (File, error)
}

// SessionReader adapts an unlocked vault to Reader.
type SessionReader struct{ S *vault.Session }

func (r SessionReader) FileSize(p string) (int64, error)  { return r.S.FileSize(p) }
func (r SessionReader) ReadFile(p string) ([]byte, error) { return r.S.ReadFile(p) }
func (r SessionReader) Open(p string) (File, error) {
	f, err := r.S.Open(p)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// Decoder renders the formats Go cannot: HEIC/HEIF, camera RAW and video
// frames. Hosts implement it with ImageIO/AVFoundation (macOS) or
// BitmapFactory/MediaMetadataRetriever (Android). Both methods return JPEG
// bytes, work only in memory, and may return ErrNoPreview.
type Decoder interface {
	Image(data []byte, ext string, maxEdge int) ([]byte, error)
	Video(f File, ext string, maxEdge int) ([]byte, error)
}

// Options configures a Service. Zero values pick the defaults noted.
type Options struct {
	// MemBytes bounds the encrypted in-memory tier. Default 64 MiB.
	MemBytes int64
	// Disk is the optional persistent tier; nil means memory only. It needs a
	// Sealer, normally the vault session itself.
	Disk   *cache.ThumbDisk
	Sealer cache.Sealer
	// MaxSourceBytes caps how large a file is decoded whole for a preview.
	// Default 100 MiB. Videos are not subject to it; they are read in ranges.
	MaxSourceBytes int64
	// Concurrency bounds simultaneous decodes. Default 2.
	Concurrency int
	// Decoder renders what Go cannot. nil means those files get ErrNoPreview
	// unless the host supplies a preview through Put.
	Decoder Decoder
}

// Service produces previews for one source through three tiers: an encrypted
// memory cache, an optional disk cache sealed with the vault key, and the
// decoders. Hosts that render a preview themselves hand it back with Put so
// both tiers learn it.
type Service struct {
	sourceID string
	r        Reader
	opts     Options
	mem      *cache.Blob
	permits  chan struct{}

	mu   sync.Mutex
	keys map[string][]string // path -> keys stored, for Invalidate
}

// New creates a Service for the source identified by sourceID (a vault path,
// a Drive folder URL: anything stable that distinguishes caches).
func New(sourceID string, r Reader, opts Options) (*Service, error) {
	if opts.MemBytes <= 0 {
		opts.MemBytes = 64 << 20
	}
	if opts.MaxSourceBytes <= 0 {
		opts.MaxSourceBytes = 100 << 20
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = 2
	}
	if opts.Disk != nil && opts.Sealer == nil {
		return nil, errors.New("thumb: a disk tier needs a Sealer")
	}
	mem, err := cache.NewBlob(opts.MemBytes)
	if err != nil {
		return nil, err
	}
	return &Service{
		sourceID: sourceID,
		r:        r,
		opts:     opts,
		mem:      mem,
		permits:  make(chan struct{}, opts.Concurrency),
		keys:     make(map[string][]string),
	}, nil
}

// Get returns a JPEG preview whose longest edge is at most maxEdge pixels.
func (s *Service) Get(ctx context.Context, p string, maxEdge int) ([]byte, error) {
	if maxEdge < 16 || maxEdge > 4096 {
		return nil, fmt.Errorf("thumb: maxEdge %d out of range", maxEdge)
	}
	size, err := s.r.FileSize(p)
	if err != nil {
		return nil, err
	}
	key := cache.ThumbKey(p, maxEdge, size)

	if jpeg, ok := s.mem.Get(key); ok {
		return jpeg, nil
	}
	if s.opts.Disk != nil {
		if jpeg, ok := s.opts.Disk.Get(s.sourceID, key, s.opts.Sealer); ok {
			s.mem.Put(key, jpeg)
			s.remember(p, key)
			return jpeg, nil
		}
	}

	select {
	case s.permits <- struct{}{}:
		defer func() { <-s.permits }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	// Another request may have filled the cache while we waited.
	if jpeg, ok := s.mem.Get(key); ok {
		return jpeg, nil
	}

	jpeg, err := s.render(p, size, maxEdge)
	if err != nil {
		return nil, err
	}
	s.store(p, key, jpeg)
	return jpeg, nil
}

// render produces a preview from the source, choosing the decoder by name.
func (s *Service) render(p string, size int64, maxEdge int) ([]byte, error) {
	name := path.Base(p)
	ext := vault.Extension(name)
	switch {
	case vault.IsBrowserDecodableImage(name):
		if size > s.opts.MaxSourceBytes {
			return nil, ErrNoPreview
		}
		src, err := s.r.ReadFile(p)
		if err != nil {
			return nil, err
		}
		jpeg, err := Make(src, maxEdge)
		wipe(src)
		if err != nil {
			return nil, ErrNoPreview
		}
		return jpeg, nil

	case vault.ClassifyMedia(name) == vault.MediaImage:
		if s.opts.Decoder == nil || size > s.opts.MaxSourceBytes {
			return nil, ErrNoPreview
		}
		src, err := s.r.ReadFile(p)
		if err != nil {
			return nil, err
		}
		jpeg, err := s.opts.Decoder.Image(src, ext, maxEdge)
		wipe(src)
		if err != nil {
			return nil, ErrNoPreview
		}
		return jpeg, nil

	case vault.ClassifyMedia(name) == vault.MediaVideo:
		if s.opts.Decoder == nil {
			return nil, ErrNoPreview
		}
		f, err := s.r.Open(p)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		jpeg, err := s.opts.Decoder.Video(f, ext, maxEdge)
		if err != nil {
			return nil, ErrNoPreview
		}
		return jpeg, nil
	}
	return nil, ErrNoPreview
}

// Put stores a preview the host rendered itself (HEIC through ImageIO, a
// video frame through AVFoundation) so later requests hit the caches.
func (s *Service) Put(p string, maxEdge int, jpeg []byte) error {
	size, err := s.r.FileSize(p)
	if err != nil {
		return err
	}
	s.store(p, cache.ThumbKey(p, maxEdge, size), jpeg)
	return nil
}

func (s *Service) store(p, key string, jpeg []byte) {
	s.mem.Put(key, jpeg)
	if s.opts.Disk != nil {
		// A disk failure costs persistence, not correctness.
		_ = s.opts.Disk.Put(s.sourceID, key, s.opts.Sealer, jpeg)
	}
	s.remember(p, key)
}

func (s *Service) remember(p, key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.keys[p] {
		if k == key {
			return
		}
	}
	s.keys[p] = append(s.keys[p], key)
}

// Invalidate drops every cached preview of p, after the file was edited,
// moved or deleted. A changed size would miss anyway; this also covers an
// edit that kept the size.
func (s *Service) Invalidate(p string) {
	s.mu.Lock()
	keys := s.keys[p]
	delete(s.keys, p)
	s.mu.Unlock()
	for _, k := range keys {
		s.mem.Remove(k)
		if s.opts.Disk != nil {
			s.opts.Disk.Invalidate(s.sourceID, k)
		}
	}
}

// Close wipes the memory tier's key. The disk tier stays, sealed with the
// vault key, which the owning session wipes on its own Close.
func (s *Service) Close() {
	s.mem.Close()
}

func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

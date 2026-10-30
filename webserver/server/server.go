package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/adrianm/imagethat/core/vault"
)

// Config configures a Server.
type Config struct {
	// Root is the directory scanned for vaults.
	Root string
	// ScanDepth limits how deep the vault scan descends below Root.
	ScanDepth int
	// IdleTimeout closes browser sessions (and wipes their vault keys) after
	// this much inactivity. Zero disables expiry.
	IdleTimeout time.Duration
	// ThumbCacheBytes bounds the per-vault encrypted thumbnail cache.
	ThumbCacheBytes int64
	// TLS marks the server as HTTPS so cookies can be flagged Secure.
	TLS bool
	// FFmpegPath enables server-side video thumbnails via this ffmpeg binary.
	// Empty disables them; the browser then captures frames itself.
	FFmpegPath string
	// Logger receives request and error output.
	Logger *log.Logger
	// Mode is ModeServerDecrypt (the default) or ModeClientDecrypt.
	Mode string
	// AccessPasswordHash is the bcrypt hash a client-decrypt server checks at
	// /api/login. Required in that mode, refused in the other.
	AccessPasswordHash []byte
	// Version is reported by /api/info.
	Version string
}

// Server modes. In server-decrypt mode the server unlocks vaults and serves
// plaintext. In client-decrypt mode it only ever serves ciphertext, behind a
// server access password, and never sees a vault password or key.
const (
	ModeServerDecrypt = "server-decrypt"
	ModeClientDecrypt = "client-decrypt"
)

// Vault is a discovered vault, as exposed to the client.
//
// The filesystem path is deliberately absent: the client addresses vaults by
// opaque ID so the API never discloses the server's directory layout.
type Vault struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Unlocked bool   `json:"unlocked"`
}

// Server serves a set of gocryptfs vaults over HTTP.
type Server struct {
	cfg      Config
	sessions *sessionStore
	limiter  *rateLimiter
	// videoThumbs is nil when no ffmpeg is available.
	videoThumbs *videoThumbnailer

	mu     sync.RWMutex
	vaults map[string]string // vault ID -> absolute path

	mux  *http.ServeMux
	stop chan struct{}
	once sync.Once
}

// New discovers the vaults under cfg.Root and returns a ready Server.
func New(cfg Config) (*Server, error) {
	if cfg.Root == "" {
		return nil, fmt.Errorf("server: Root is required")
	}
	root, err := filepath.Abs(cfg.Root)
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("server: root %q is not a directory", cfg.Root)
	}
	cfg.Root = root
	if cfg.ScanDepth <= 0 {
		cfg.ScanDepth = 3
	}
	if cfg.ThumbCacheBytes <= 0 {
		cfg.ThumbCacheBytes = 256 << 20
	}
	switch cfg.Mode {
	case "":
		cfg.Mode = ModeServerDecrypt
	case ModeServerDecrypt, ModeClientDecrypt:
	default:
		return nil, fmt.Errorf("server: unknown mode %q", cfg.Mode)
	}
	if cfg.Mode == ModeClientDecrypt && len(cfg.AccessPasswordHash) == 0 {
		return nil, fmt.Errorf("server: %s mode needs an access password", ModeClientDecrypt)
	}
	if cfg.Mode == ModeServerDecrypt && len(cfg.AccessPasswordHash) != 0 {
		return nil, fmt.Errorf("server: an access password is only used in %s mode", ModeClientDecrypt)
	}
	if cfg.Logger == nil {
		cfg.Logger = log.New(os.Stderr, "", log.LstdFlags)
	}

	s := &Server{
		cfg:      cfg,
		sessions: newSessionStore(cfg.IdleTimeout),
		limiter:  newRateLimiter(10, time.Minute),
		stop:     make(chan struct{}),
	}
	if cfg.FFmpegPath != "" {
		s.videoThumbs = newVideoThumbnailer(cfg.FFmpegPath)
	}
	if err := s.Rescan(); err != nil {
		return nil, err
	}
	s.routes()
	go s.reaper()
	return s, nil
}

// Close stops background work and wipes every in-memory vault key.
func (s *Server) Close() {
	s.once.Do(func() {
		close(s.stop)
		s.sessions.closeAll()
	})
}

// reaper expires idle sessions so an abandoned browser tab does not keep a
// vault unlocked forever.
func (s *Server) reaper() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			s.sessions.reapExpired()
		}
	}
}

// vaultID derives a stable opaque ID from a vault's absolute path.
func vaultID(absPath string) string {
	sum := sha256.Sum256([]byte(absPath))
	return hex.EncodeToString(sum[:8])
}

// Rescan re-walks Root looking for directories that contain gocryptfs.conf.
func (s *Server) Rescan() error {
	found := make(map[string]string)

	err := filepath.WalkDir(s.cfg.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable subtree should not abort the whole scan.
			return nil //nolint:nilerr // deliberately tolerant
		}
		if !d.IsDir() {
			return nil
		}
		if depth(s.cfg.Root, p) > s.cfg.ScanDepth {
			return fs.SkipDir
		}
		if name := d.Name(); name != "." && strings.HasPrefix(name, ".") && p != s.cfg.Root {
			return fs.SkipDir
		}
		if vault.IsVault(p) {
			found[vaultID(p)] = p
			// A vault's ciphertext contains no nested vaults worth showing.
			return fs.SkipDir
		}
		return nil
	})
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.vaults = found
	s.mu.Unlock()
	return nil
}

// depth counts path components between root and p.
func depth(root, p string) int {
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == "." {
		return 0
	}
	return len(strings.Split(rel, string(filepath.Separator)))
}

// vaultPath resolves a vault ID to its absolute path.
func (s *Server) vaultPath(id string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.vaults[id]
	return p, ok
}

// listVaults returns every discovered vault, marking the ones this browser
// session has unlocked.
func (s *Server) listVaults(b *browserSession) []Vault {
	s.mu.RLock()
	paths := make(map[string]string, len(s.vaults))
	for id, p := range s.vaults {
		paths[id] = p
	}
	s.mu.RUnlock()

	var unlocked map[string]bool
	if b != nil {
		unlocked = b.unlockedIDs()
	}

	out := make([]Vault, 0, len(paths))
	for id, p := range paths {
		name := filepath.Base(p)
		if p == s.cfg.Root {
			name = filepath.Base(s.cfg.Root)
		}
		out = append(out, Vault{ID: id, Name: name, Unlocked: unlocked[id]})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		s.mux.ServeHTTP(w, r)
		return
	}
	// API requests get a log line with the outcome. The query string names
	// plaintext vault paths, which this log deliberately includes — it goes to
	// the same place as Session.Logf diagnostics, which already do.
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	start := time.Now()
	s.mux.ServeHTTP(rec, r)
	s.cfg.Logger.Printf("%s %s -> %d (%s)", r.Method, r.URL.RequestURI(), rec.status, time.Since(start).Round(time.Millisecond))
}

// statusRecorder captures the response status for request logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// writeJSON sends a JSON response with caching disabled.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// Listings name vault contents, so they are as sensitive as the files.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// apiError is the error shape every failing endpoint returns.
type apiError struct {
	Error string `json:"error"`
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, apiError{Error: msg})
}

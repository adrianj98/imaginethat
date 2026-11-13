package server

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/adrianm/imagethat/core/faulttest"
	"github.com/adrianm/imagethat/core/remote"
	"github.com/adrianm/imagethat/core/vault"
	"github.com/adrianm/imagethat/core/xfer"
)

// The copy fault table, over the network: the Go client the Mac and Android
// apps use, against a real client-decrypt server, with a fault proxy on the
// wire between them.

// requestTimeout is the client's per-request limit in these scenarios. A
// stall shorter than it must pass unnoticed; a blackhole must end at it.
const (
	requestTimeout = 500 * time.Millisecond
	shortStall     = 100 * time.Millisecond
)

// faultServer starts a client-decrypt server over root, which must already
// hold a vault at root/myvault. Calling it again on the same root is a
// server restart: new sessions, same files.
func faultServer(t *testing.T, root string) *Server {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(testAccessPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(Config{
		Root:               root,
		ScanDepth:          3,
		ThumbCacheBytes:    8 << 20,
		Logger:             log.New(io.Discard, "", 0),
		Mode:               ModeClientDecrypt,
		AccessPasswordHash: hash,
		Version:            "test",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(srv.Close)
	return srv
}

// serve puts srv on a local port. Its TCP keepalive is set to seconds where
// Go's default is 15s and more, so a connection that died without the server
// hearing of it is found quickly and the scenario does not wait on it.
func serve(t *testing.T, srv *Server) *httptest.Server {
	t.Helper()
	lc := net.ListenConfig{KeepAliveConfig: net.KeepAliveConfig{
		Enable: true, Idle: time.Second, Interval: time.Second, Count: 1,
	}}
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(srv)
	ts.Listener.Close()
	ts.Listener = ln
	ts.Start()
	return ts
}

// serverSide is a running server, the proxy in front of it and a client
// session that reaches it through the proxy.
type serverSide struct {
	root    string // the server's Root; the vault is root/myvault
	srv     *Server
	ts      *httptest.Server
	proxy   *faulttest.Proxy
	session *vault.Session

	mu sync.Mutex
}

func (ss *serverSide) vaultDir() string { return filepath.Join(ss.root, "myvault") }

// restart replaces the server process: the old one is shut down, a new one
// starts on the same files with no memory of any session, and the proxy is
// pointed at it.
func (ss *serverSide) restart(t *testing.T) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	// A process that dies takes its connections with it; it does not wait
	// for requests in flight to finish.
	ss.ts.CloseClientConnections()
	ss.ts.Close()
	ss.srv.Close()
	ss.srv = faultServer(t, ss.root)
	ss.ts = serve(t, ss.srv)
	t.Cleanup(ss.ts.Close)
	ss.proxy.SetTarget(ss.ts.Listener.Addr().String())
}

// newServerSide builds the server and its client. prepare, if set, runs on
// the vault directory before the server starts. transport, if set, replaces
// the client's HTTP transport.
func newServerSide(t *testing.T, prepare func(dir string), transport http.RoundTripper) *serverSide {
	t.Helper()
	ss := &serverSide{root: t.TempDir()}
	faultTemplate(t).Dir(t, ss.vaultDir())
	if prepare != nil {
		prepare(ss.vaultDir())
	}
	ss.srv = faultServer(t, ss.root)
	ss.ts = serve(t, ss.srv)
	t.Cleanup(func() {
		ss.mu.Lock()
		defer ss.mu.Unlock()
		ss.ts.Close()
	})
	ss.proxy = faulttest.NewProxy(t, ss.ts.Listener.Addr().String())

	hc := newClient(t, ss.srv)
	hc.login(testAccessPassword)
	id := hc.vaultID()

	ctx := context.Background()
	rc, err := remote.New(ss.proxy.URL(), remote.Options{Timeout: requestTimeout, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if err := rc.Login(ctx, testAccessPassword); err != nil {
		t.Fatalf("Login: %v", err)
	}
	unlockMu.Lock()
	ss.session, err = rc.OpenVault(ctx, id, faultTemplate(t).Password)
	unlockMu.Unlock()
	if err != nil {
		t.Fatalf("OpenVault: %v", err)
	}
	t.Cleanup(func() { ss.session.Close() })
	return ss
}

func serverUpRig(t *testing.T, size int, transport http.RoundTripper) (*rig, *serverSide) {
	want := payload(size)
	src := writeSource(t, want)
	ss := newServerSide(t, nil, transport)
	if err := ss.session.Mkdir(filepath.Dir(destPath)); err != nil {
		t.Fatal(err)
	}
	r := &rig{want: want, opts: faultOpts(), proxy: ss.proxy, heal: ss.proxy.Heal}
	r.copy = func(ctx context.Context) error {
		_, err := ss.session.CopyIn(ctx, destPath, xfer.OpenFile(src), r.opts)
		return err
	}
	r.got, r.entries = localReaders(t, ss.vaultDir())
	r.leftovers = func() []string { return tempsUnder(ss.root) }
	return r, ss
}

// serverUp copies a plain file into a vault on a server.
var serverUp = copyPath{name: "server up", setup: func(t *testing.T, size int) *rig {
	r, _ := serverUpRig(t, size, nil)
	return r
}}

// serverDown copies a file out of a vault on a server into a local vault.
var serverDown = copyPath{name: "server down", setup: func(t *testing.T, size int) *rig {
	want := payload(size)
	src := writeSource(t, want)
	ss := newServerSide(t, func(dir string) {
		// Put the file in the server's vault before the server starts.
		s, err := unlockLocal(t, dir)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if err := s.Mkdir(filepath.Dir(destPath)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.CopyIn(context.Background(), destPath, xfer.OpenFile(src), xfer.Options{}); err != nil {
			t.Fatal(err)
		}
	}, nil)

	dir := newLocalVault(t)
	local, err := unlockLocal(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { local.Close() })
	if err := local.Mkdir(filepath.Dir(destPath)); err != nil {
		t.Fatal(err)
	}
	r := &rig{want: want, opts: faultOpts(), proxy: ss.proxy, heal: ss.proxy.Heal}
	r.copy = func(ctx context.Context) error {
		_, err := local.CopyIn(ctx, destPath, ss.session.Opener(destPath), r.opts)
		return err
	}
	r.got, r.entries = localReaders(t, dir)
	r.leftovers = func() []string { return append(tempsUnder(dir), tempsUnder(ss.root)...) }
	return r
}}

func armProxy(f faulttest.Fault) func(*testing.T, *rig) {
	return func(_ *testing.T, r *rig) { r.proxy.Arm(f) }
}

// healAfter arms f and heals the proxy d after it fires.
func healAfter(f faulttest.Fault, d time.Duration) func(*testing.T, *rig) {
	return func(_ *testing.T, r *rig) {
		r.proxy.Arm(f)
		fired := r.proxy.FiredCh()
		go func() {
			<-fired
			time.Sleep(d)
			r.proxy.Heal()
		}()
	}
}

// wireFaults are the scenarios every network path shares. dir is the
// direction the file's bytes travel in.
func wireFaults(size int, dir faulttest.Dir) []scenario {
	var out []scenario
	for where, off := range map[string]int64{"early": 8 << 10, "middle": int64(size / 2), "late": int64(size - 1)} {
		out = append(out,
			scenario{
				name: "connection closed, " + where,
				arm:  armProxy(faulttest.Fault{Dir: dir, At: off, Action: faulttest.Close}),
				want: recovers,
			},
			scenario{
				name: "connection reset, " + where,
				arm:  armProxy(faulttest.Fault{Dir: dir, At: off, Action: faulttest.Reset}),
				want: recovers,
			})
	}
	mid := int64(size / 2)
	return append(out,
		scenario{
			name: "stall shorter than the timeout",
			arm:  armProxy(faulttest.Fault{Dir: dir, At: mid, Action: faulttest.Stall, For: shortStall}),
			want: recovers,
		},
		scenario{
			name: "blackhole",
			arm:  healAfter(faulttest.Fault{Dir: dir, At: mid, Action: faulttest.Blackhole}, requestTimeout),
			want: recovers,
		},
		scenario{
			name: "short outage",
			arm:  armProxy(faulttest.Fault{Dir: dir, At: mid, Action: faulttest.Outage, For: blip}),
			want: recovers,
		},
		scenario{
			name: "outage past the retry window",
			arm:  armProxy(faulttest.Fault{Dir: dir, At: mid, Action: faulttest.Outage}),
			want: failsCleanly,
		},
		scenario{
			name: "reset twice",
			arm: func(_ *testing.T, r *rig) {
				again := faulttest.Fault{Dir: dir, At: mid, Action: faulttest.Reset}
				r.proxy.Arm(again)
				fired := r.proxy.FiredCh()
				go func() {
					<-fired
					r.proxy.Arm(again)
				}()
			},
			want: recovers,
		},
	)
}

func TestCopyFaultsServer(t *testing.T) {
	const size = faultSize
	for _, sc := range wireFaults(size, faulttest.Down) {
		runScenario(t, serverDown, size, sc)
	}
	runScenario(t, serverDown, size, scenario{
		name: "truncated response",
		arm:  armProxy(faulttest.Truncate(size / 2)),
		want: recovers,
	})
	for _, sc := range wireFaults(size, faulttest.Up) {
		runScenario(t, serverUp, size, sc)
	}
}

// The server renamed the upload into place, and its answer never arrived.
// The retry finds the file there and must count the copy as done, once.
func TestCopyFaultsServerCommitReplyLost(t *testing.T) {
	var dropped atomic.Bool
	lostReply := copyPath{name: "server up", setup: func(t *testing.T, size int) *rig {
		rt := roundTripper(func(req *http.Request) (*http.Response, error) {
			resp, err := http.DefaultTransport.RoundTrip(req)
			if err != nil || req.Method != http.MethodPost || !strings.HasSuffix(req.URL.Path, "/raw/rename") ||
				!dropped.CompareAndSwap(false, true) {
				return resp, err
			}
			resp.Body.Close()
			return nil, io.ErrUnexpectedEOF
		})
		r, _ := serverUpRig(t, size, rt)
		r.also = dropped.Load
		return r
	}}
	runScenario(t, lostReply, faultSize, scenario{
		name:  "commit landed, reply lost",
		arm:   func(*testing.T, *rig) {},
		want:  recovers,
		check: oneEntry,
	})
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// The server process goes away in the middle of an upload and a new one
// comes up on the same files: no sessions, and whatever the old one was
// writing is orphaned.
func TestCopyFaultsServerRestart(t *testing.T) {
	var ss *serverSide
	restarting := copyPath{name: "server up", setup: func(t *testing.T, size int) *rig {
		var r *rig
		r, ss = serverUpRig(t, size, nil)
		return r
	}}
	runScenario(t, restarting, faultSize, scenario{
		name: "server restarts mid-upload",
		arm: func(t *testing.T, r *rig) {
			// A restart takes longer than a blip, and longer still when
			// the whole suite is running; give it a window of seconds.
			r.opts.Attempts = 250
			r.proxy.Arm(faulttest.Fault{Dir: faulttest.Up, At: faultSize / 2, Action: faulttest.Outage})
			fired := r.proxy.FiredCh()
			go func() {
				<-fired
				ss.restart(t)
				r.proxy.Heal()
			}()
			// The old process had no chance to tidy up, so its one temp
			// file may stay; stale-temp cleanup removes it after a day.
			all := r.leftovers
			r.leftovers = func() []string {
				left := all()
				if len(left) <= 1 {
					return nil
				}
				return left
			}
		},
		want: recovers,
	})
}

// Cancelling while nothing is moving must return at once and leave nothing.
func TestCopyFaultsServerCancelDuringStall(t *testing.T) {
	r, _ := serverUpRig(t, faultSize, nil)
	r.proxy.Arm(faulttest.Fault{Dir: faulttest.Up, At: faultSize / 2, Action: faulttest.Stall, For: time.Hour})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.copy(ctx) }()

	select {
	case <-r.proxy.FiredCh():
	case <-time.After(5 * time.Second):
		t.Fatal("the stall never started")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("copy returned %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("the copy did not return within 1s of being cancelled")
	}
	r.proxy.Heal()
	r.assertNothing(t)
}

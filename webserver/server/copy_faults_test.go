package server

import (
	"context"
	"crypto/sha256"
	"errors"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/adrianm/imagethat/core/faulttest"
	"github.com/adrianm/imagethat/core/vault"
	"github.com/adrianm/imagethat/core/xfer"
)

// The copy fault table. Each scenario arms one fault on one copy path and
// declares how the copy must end:
//
//   - recovers: the destination decrypts to the source's exact bytes and no
//     temp file is left anywhere;
//   - failsCleanly: an error comes back, nothing is at the destination, no
//     temp file is left, and once the fault is healed a re-run recovers.
//
// A copy that outlives its deadline is a failure in its own right, and so is
// a scenario whose fault never fired.

type outcome int

const (
	recovers outcome = iota
	failsCleanly
)

const (
	// Several megabytes, so a copy takes more than one read, plus a partial
	// block at the end.
	faultSize = 3<<20 + 123
	destPath  = "in/file.bin"
	tempMark  = ".imagethat-"
)

// faultOpts keeps the engine's retry shape and shrinks its waits: nine waits
// adding up to about 110ms stand for the real five minutes.
func faultOpts() xfer.Options {
	return xfer.Options{Attempts: 10, BaseDelay: time.Millisecond, MaxDelay: 20 * time.Millisecond}
}

// blip is a fault that clears well inside the retry window.
const blip = 30 * time.Millisecond

func payload(size int) []byte {
	r := rand.New(rand.NewPCG(1, 2))
	b := make([]byte, size)
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	return b
}

// rig is one prepared copy: a source, a destination and the places faults
// go in.
type rig struct {
	want []byte
	// copy runs the copy once.
	copy func(ctx context.Context) error
	// got reads the destination back through a second, clean session.
	got func() ([]byte, error)
	// entries lists the destination directory, through a clean session.
	entries func() ([]string, error)
	// leftovers lists temp files at the destination and on any server.
	leftovers func() []string
	// heal clears every fault.
	heal func()

	// opts are the retry settings the copy runs with. A scenario may widen
	// them in arm.
	opts xfer.Options

	proxy *faulttest.Proxy
	fs    *faulttest.FS
	flaky *faulttest.Flaky
	// also reports a fault that none of the three above carries.
	also func() bool
}

func (r *rig) fired() bool {
	return (r.proxy != nil && r.proxy.Fired()) || (r.fs != nil && r.fs.Fired()) ||
		(r.flaky != nil && r.flaky.Fired()) || (r.also != nil && r.also())
}

type copyPath struct {
	name  string
	setup func(t *testing.T, size int) *rig
}

type scenario struct {
	name string
	// arm arms the fault. It runs after setup and before the copy.
	arm  func(t *testing.T, r *rig)
	want outcome
	// wantErr, if set, is what a failsCleanly error must match.
	wantErr error
	// deadline is how long the copy may take before it counts as hung.
	// Zero means 10s.
	deadline time.Duration
	// check runs extra assertions after a recovery.
	check func(t *testing.T, r *rig)
	// unfiredOK is for the rare case where the fault cannot fire.
	unfiredOK bool
	// skip names the later piece of work a scenario waits for.
	skip string
}

// runCopy runs the copy and fails the test if it outlives the deadline.
func runCopy(t *testing.T, r *rig, deadline time.Duration) (error, time.Duration) {
	t.Helper()
	if deadline == 0 {
		deadline = 10 * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- r.copy(ctx) }()
	select {
	case err := <-done:
		return err, time.Since(start)
	case <-time.After(deadline):
		t.Fatalf("the copy hung: no result after %v", deadline)
		return nil, 0
	}
}

func (r *rig) assertCopied(t *testing.T) {
	t.Helper()
	got, err := r.got()
	if err != nil {
		t.Fatalf("reading the destination back: %v", err)
	}
	if sha256.Sum256(got) != sha256.Sum256(r.want) {
		t.Errorf("destination differs from the source: %d bytes, want %d", len(got), len(r.want))
	}
	r.assertNoLeftovers(t)
}

// tidyWait is how long a server gets to notice a dead upload and remove its
// temp file. A connection that dies without a reset reaching the server (a
// client that drops off the network, and sometimes this proxy's reset under
// load) is only found by TCP keepalive, which Go's servers send after 15s.
const tidyWait = 30 * time.Second

func (r *rig) assertNoLeftovers(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(tidyWait)
	left := r.leftovers()
	for len(left) > 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		left = r.leftovers()
	}
	if len(left) > 0 {
		t.Errorf("temp files still there after %v: %v", tidyWait, left)
	}
}

func (r *rig) assertNothing(t *testing.T) {
	t.Helper()
	if got, err := r.got(); !errors.Is(err, vault.ErrNotFound) {
		t.Errorf("destination after a failed copy: %d bytes, err %v; want not found", len(got), err)
	}
	r.assertNoLeftovers(t)
}

func runScenario(t *testing.T, p copyPath, size int, sc scenario) {
	t.Run(p.name+"/"+sc.name, func(t *testing.T) {
		if sc.skip != "" {
			t.Skip(sc.skip)
		}
		// Scenarios share nothing, and unlocking a vault is CPU-bound.
		t.Parallel()
		r := p.setup(t, size)
		sc.arm(t, r)
		err, _ := runCopy(t, r, sc.deadline)
		if !r.fired() && !sc.unfiredOK {
			t.Fatalf("the fault never fired, so this proves nothing (copy returned %v)", err)
		}
		switch sc.want {
		case recovers:
			if err != nil {
				t.Fatalf("copy failed: %v", err)
			}
			r.assertCopied(t)
			if sc.check != nil {
				sc.check(t, r)
			}
		case failsCleanly:
			if err == nil {
				t.Fatal("copy reported success")
			}
			if sc.wantErr != nil && !errors.Is(err, sc.wantErr) {
				t.Errorf("copy failed with %v, want %v", err, sc.wantErr)
			}
			r.assertNothing(t)
			r.heal()
			if err, _ := runCopy(t, r, sc.deadline); err != nil {
				t.Fatalf("re-run after healing: %v", err)
			}
			r.assertCopied(t)
		}
	})
}

// oneEntry checks a commit that landed twice did not leave two files.
func oneEntry(t *testing.T, r *rig) {
	t.Helper()
	names, err := r.entries()
	if err != nil || len(names) != 1 {
		t.Errorf("destination directory holds %v (err %v), want exactly one file", names, err)
	}
}

// faultVault is the empty vault every scenario gets a copy of. It is cheap
// to unlock (see faulttest.Template), which is most of what a scenario costs
// under the race detector.
var (
	faultVaultOnce sync.Once
	faultVault     *faulttest.Template
)

func faultTemplate(t *testing.T) *faulttest.Template {
	t.Helper()
	faultVaultOnce.Do(func() { faultVault = faulttest.NewTemplate(t, "fault-table-password") })
	if faultVault == nil {
		t.Fatal("the vault template could not be made (see the first failing test)")
	}
	return faultVault
}

// unlockMu makes the scenarios unlock one at a time. Unlocking flips a
// package-level logging flag inside the gocryptfs code (tlog.Warn.Enabled in
// ConfFile.DecryptMasterKey), so two unlocks at once are a data race there.
var unlockMu sync.Mutex

func unlock(t *testing.T, v vault.VFS) (*vault.Session, error) {
	pw := faultTemplate(t).Password
	unlockMu.Lock()
	defer unlockMu.Unlock()
	return vault.Open(v, pw)
}

func unlockLocal(t *testing.T, dir string) (*vault.Session, error) {
	return unlock(t, vault.NewLocalFS(dir))
}

// --- local filesystem paths ---

func writeSource(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.bin")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// newLocalVault is a new, empty vault on disk.
func newLocalVault(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "vault")
	faultTemplate(t).Dir(t, dir)
	return dir
}

// tempsUnder lists upload temp files anywhere below dir.
func tempsUnder(dir string) []string {
	var out []string
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasPrefix(info.Name(), tempMark) {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// localReaders are got and entries for a vault directory on disk, through
// one session of their own, opened on first use. (Unlocking is the slow part
// of a scenario, so it is not done per call.)
func localReaders(t *testing.T, dir string) (got func() ([]byte, error), entries func() ([]string, error)) {
	var s *vault.Session
	open := func() (*vault.Session, error) {
		if s != nil {
			return s, nil
		}
		opened, err := unlockLocal(t, dir)
		if err != nil {
			return nil, err
		}
		s = opened
		t.Cleanup(func() { opened.Close() })
		return s, nil
	}
	got = func() ([]byte, error) {
		s, err := open()
		if err != nil {
			return nil, err
		}
		return s.ReadFile(destPath)
	}
	entries = func() ([]string, error) {
		s, err := open()
		if err != nil {
			return nil, err
		}
		list, err := s.ListDir(filepath.Dir(destPath))
		var names []string
		for _, e := range list {
			names = append(names, e.Name)
		}
		return names, err
	}
	return got, entries
}

// localIn copies a plain file into a vault whose storage misbehaves.
var localIn = copyPath{name: "local in", setup: func(t *testing.T, size int) *rig {
	want := payload(size)
	src := writeSource(t, want)
	dir := newLocalVault(t)
	ffs := faulttest.NewFS(vault.NewLocalFS(dir))
	s, err := unlock(t, ffs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Mkdir(filepath.Dir(destPath)); err != nil {
		t.Fatal(err)
	}
	r := &rig{want: want, opts: faultOpts(), fs: ffs, heal: ffs.Clear}
	r.copy = func(ctx context.Context) error {
		_, err := s.CopyIn(ctx, destPath, xfer.OpenFile(src), r.opts)
		return err
	}
	r.got, r.entries = localReaders(t, dir)
	r.leftovers = func() []string { return tempsUnder(dir) }
	return r
}}

// mountIn copies a file from a mount that misbehaves into a healthy vault.
var mountIn = copyPath{name: "mount in", setup: func(t *testing.T, size int) *rig {
	want := payload(size)
	src := writeSource(t, want)
	dir := newLocalVault(t)
	s, err := unlockLocal(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Mkdir(filepath.Dir(destPath)); err != nil {
		t.Fatal(err)
	}
	open, flaky := faulttest.FlakyOpener(xfer.OpenFile(src), faulttest.ReadFault{})
	r := &rig{want: want, opts: faultOpts(), flaky: flaky, heal: flaky.Clear}
	r.copy = func(ctx context.Context) error {
		_, err := s.CopyIn(ctx, destPath, open, r.opts)
		return err
	}
	r.got, r.entries = localReaders(t, dir)
	r.leftovers = func() []string { return tempsUnder(dir) }
	return r
}}

// at names the three places a fault is tried: the first block, the middle,
// and the last byte.
func at(size int) map[string]int64 {
	return map[string]int64{"early": 4096, "middle": int64(size / 2), "late": int64(size - 1)}
}

func readFault(rf faulttest.ReadFault) func(*testing.T, *rig) {
	return func(_ *testing.T, r *rig) { r.flaky.Set(rf) }
}

func TestCopyFaultsFilesystem(t *testing.T) {
	const size = faultSize
	for where, off := range at(size) {
		if where == "early" {
			off = 0
		}
		runScenario(t, mountIn, size, scenario{
			name: "read error, " + where,
			arm:  readFault(faulttest.ReadFault{From: off, Calls: 3, Err: syscall.EIO}),
			want: recovers,
		})
	}
	runScenario(t, mountIn, size, scenario{
		name: "reads time out for a while",
		arm:  readFault(faulttest.ReadFault{From: size / 2, For: blip, Err: syscall.ETIMEDOUT}),
		want: recovers,
	})
	runScenario(t, mountIn, size, scenario{
		name: "short reads",
		arm:  readFault(faulttest.ReadFault{From: size / 2, Calls: 3, Short: true}),
		want: recovers,
	})
	runScenario(t, mountIn, size, scenario{
		name: "outage past the retry window",
		arm:  readFault(faulttest.ReadFault{From: size / 2, For: time.Hour, Err: syscall.EIO}),
		want: failsCleanly,
	})

	for where, off := range at(size) {
		runScenario(t, localIn, size, scenario{
			name: "write fails, " + where,
			arm:  func(_ *testing.T, r *rig) { r.fs.FailWrites(2, off, syscall.EIO) },
			want: recovers,
		})
	}
	runScenario(t, localIn, size, scenario{
		name: "close fails",
		arm:  func(_ *testing.T, r *rig) { r.fs.FailClose(2, syscall.EIO) },
		want: recovers,
	})
	runScenario(t, localIn, size, scenario{
		name: "rename fails",
		arm:  func(_ *testing.T, r *rig) { r.fs.FailRename(2, false, syscall.EIO) },
		want: recovers,
	})
	runScenario(t, localIn, size, scenario{
		name:  "commit landed, reply lost",
		arm:   func(_ *testing.T, r *rig) { r.fs.FailRename(1, true, syscall.ETIMEDOUT) },
		want:  recovers,
		check: oneEntry,
	})
	runScenario(t, localIn, size, scenario{
		name: "disk full",
		// Armed for one file only: a copy that retried a full disk would
		// get through on its second try and report success.
		arm:     func(_ *testing.T, r *rig) { r.fs.FailWrites(1, size/2, syscall.ENOSPC) },
		want:    failsCleanly,
		wantErr: syscall.ENOSPC,
	})
	runScenario(t, localIn, size, scenario{
		name: "writes fail for good",
		arm:  func(_ *testing.T, r *rig) { r.fs.FailWrites(1000, size/2, syscall.EIO) },
		want: failsCleanly,
	})
}

// An empty file, a single byte, and files that end exactly on a block.
func TestCopyFaultsOddSizes(t *testing.T) {
	for _, size := range []int{0, 1, 4096, 4096 * 3} {
		name := "size " + strconv.Itoa(size)
		runScenario(t, localIn, size, scenario{
			name:  name + ", rename fails",
			arm:   func(_ *testing.T, r *rig) { r.fs.FailRename(1, false, syscall.EIO) },
			want:  recovers,
			check: oneEntry,
		})
		runScenario(t, mountIn, size, scenario{
			name: name + ", read error",
			arm:  readFault(faulttest.ReadFault{From: 0, Calls: 1, Err: syscall.EIO}),
			want: recovers,
			// An empty file is never read, so there is no read to fail.
			unfiredOK: size == 0,
		})
	}
}

// A source that changes size while it is being copied must stop the copy:
// the bytes already taken may not belong with the rest.
func TestCopyFaultsSourceShrinks(t *testing.T) {
	want := payload(faultSize)
	src := writeSource(t, want)
	dir := newLocalVault(t)
	s, err := unlockLocal(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Mkdir(filepath.Dir(destPath)); err != nil {
		t.Fatal(err)
	}

	// One failed read forces a reopen, and the file is shorter by then.
	opens := 0
	shrinking := func() (xfer.Source, error) {
		opens++
		if opens == 2 {
			if err := os.Truncate(src, faultSize-1000); err != nil {
				return nil, err
			}
		}
		return xfer.OpenFile(src)()
	}
	open, flaky := faulttest.FlakyOpener(shrinking, faulttest.ReadFault{From: faultSize / 2, Calls: 1, Err: syscall.EIO})

	r := &rig{want: want, opts: faultOpts(), flaky: flaky}
	r.copy = func(ctx context.Context) error {
		_, err := s.CopyIn(ctx, destPath, open, r.opts)
		return err
	}
	r.got, r.entries = localReaders(t, dir)
	r.leftovers = func() []string { return tempsUnder(dir) }

	err, _ = runCopy(t, r, 0)
	if !errors.Is(err, xfer.ErrSourceChanged) {
		t.Fatalf("copy returned %v, want ErrSourceChanged", err)
	}
	if !flaky.Fired() || opens < 2 {
		t.Fatalf("the source was never reopened (opens %d)", opens)
	}
	r.assertNothing(t)
}

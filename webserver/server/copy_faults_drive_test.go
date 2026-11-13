package server

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/adrianm/imagethat/core/drive"
	"github.com/adrianm/imagethat/core/drive/drivetest"
	"github.com/adrianm/imagethat/core/faulttest"
	"github.com/adrianm/imagethat/core/vault"
	"github.com/adrianm/imagethat/core/xfer"
)

// The copy fault table against Google Drive: the real Drive client and vault
// storage, a fake Drive server, and the fault proxy between them.

// driveBigSize is over one 8 MiB upload chunk, so the upload is resumable and
// goes up in two parts. A file under one chunk goes up in a single request.
const driveBigSize = 9<<20 + 123

// driveSide is a fake Drive holding a vault, a faulty client that reaches
// it through the proxy, and a way to look at it without the proxy.
type driveSide struct {
	d      *drivetest.Server
	proxy  *faulttest.Proxy
	rootID string
	// session is the vault through the proxy.
	session *vault.Session
	// clean is a Drive client that does not go through the proxy.
	clean drive.Config
}

// newDriveSide builds the fake Drive and its vault. prepare, if set, runs
// before the faulty session opens: that session caches the listings it reads
// when it unlocks, as the apps' sessions do, and would not see a file put
// there after it.
func newDriveSide(t *testing.T, prepare func(ds *driveSide)) *driveSide {
	t.Helper()
	ds := &driveSide{d: drivetest.New()}
	t.Cleanup(ds.d.Close)
	ds.clean = ds.d.Config()
	ds.proxy = faulttest.NewProxy(t, ds.d.Listener.Addr().String())
	// Upload session links now point at the proxy too.
	ds.d.PublicURL = ds.proxy.URL()

	ds.rootID = ds.d.AddFolder("root", "Vault")
	if err := faultTemplate(t).WriteTo(drive.NewVaultFS(drive.New(ds.clean), ds.rootID, nil)); err != nil {
		t.Fatalf("writing the vault to Drive: %v", err)
	}
	if prepare != nil {
		prepare(ds)
	}

	faulty := ds.d.Config()
	faulty.HTTPClient = &http.Client{Timeout: requestTimeout}
	var err error
	ds.session, err = unlock(t, drive.NewVaultFS(drive.New(faulty), ds.rootID, nil))
	if err != nil {
		t.Fatalf("unlock over Drive: %v", err)
	}
	t.Cleanup(func() { ds.session.Close() })
	return ds
}

// cleanSession opens the vault again, past the proxy and with nothing
// cached, to see what Drive really holds.
func (ds *driveSide) cleanSession(t *testing.T) (*vault.Session, error) {
	return unlock(t, drive.NewVaultFS(drive.New(ds.clean), ds.rootID, nil))
}

func (ds *driveSide) readers(t *testing.T) (got func() ([]byte, error), entries func() ([]string, error)) {
	got = func() ([]byte, error) {
		s, err := ds.cleanSession(t)
		if err != nil {
			return nil, err
		}
		defer s.Close()
		return s.ReadFile(destPath)
	}
	entries = func() ([]string, error) {
		s, err := ds.cleanSession(t)
		if err != nil {
			return nil, err
		}
		defer s.Close()
		list, err := s.ListDir(filepath.Dir(destPath))
		var names []string
		for _, e := range list {
			names = append(names, e.Name)
		}
		return names, err
	}
	return got, entries
}

// leftovers lists upload temp files anywhere in the vault's Drive folder.
func (ds *driveSide) leftovers() []string {
	var out []string
	var walk func(id, at string)
	walk = func(id, at string) {
		for _, name := range ds.d.Children(id) {
			if strings.HasPrefix(name, tempMark) {
				out = append(out, at+"/"+name)
				continue
			}
			walk(ds.d.Lookup(id, name), at+"/"+name)
		}
	}
	walk(ds.rootID, "")
	return out
}

func driveUpRig(t *testing.T, size int) (*rig, *driveSide) {
	want := payload(size)
	src := writeSource(t, want)
	ds := newDriveSide(t, nil)
	if err := ds.session.Mkdir(filepath.Dir(destPath)); err != nil {
		t.Fatal(err)
	}
	r := &rig{want: want, opts: faultOpts(), proxy: ds.proxy, heal: ds.proxy.Heal}
	r.copy = func(ctx context.Context) error {
		_, err := ds.session.CopyIn(ctx, destPath, xfer.OpenFile(src), r.opts)
		return err
	}
	r.got, r.entries = ds.readers(t)
	r.leftovers = ds.leftovers
	return r, ds
}

// driveUp copies a plain file into a vault on Drive.
var driveUp = copyPath{name: "drive up", setup: func(t *testing.T, size int) *rig {
	r, _ := driveUpRig(t, size)
	return r
}}

// putOnDrive writes the scenario's file into ds's vault, past the proxy.
func putOnDrive(t *testing.T, ds *driveSide, want []byte) {
	t.Helper()
	s, err := ds.cleanSession(t)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Mkdir(filepath.Dir(destPath)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CopyIn(context.Background(), destPath, xfer.OpenFile(writeSource(t, want)), xfer.Options{}); err != nil {
		t.Fatal(err)
	}
}

// driveDown copies a file out of a vault on Drive into a local vault.
var driveDown = copyPath{name: "drive down", setup: func(t *testing.T, size int) *rig {
	want := payload(size)
	ds := newDriveSide(t, func(ds *driveSide) { putOnDrive(t, ds, want) })

	dir := newLocalVault(t)
	local, err := unlockLocal(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { local.Close() })
	if err := local.Mkdir(filepath.Dir(destPath)); err != nil {
		t.Fatal(err)
	}
	r := &rig{want: want, opts: faultOpts(), proxy: ds.proxy, heal: ds.proxy.Heal}
	r.copy = func(ctx context.Context) error {
		_, err := local.CopyIn(ctx, destPath, ds.session.Opener(destPath), r.opts)
		return err
	}
	r.got, r.entries = localReaders(t, dir)
	r.leftovers = func() []string { return append(tempsUnder(dir), ds.leftovers()...) }
	return r
}}

// across copies from a vault on a server to a vault on Drive. The fault is
// on the server's link when onSource is set, else on Drive's.
func across(onSource bool) copyPath {
	name := "server to drive, fault on drive"
	if onSource {
		name = "server to drive, fault on server"
	}
	return copyPath{name: name, setup: func(t *testing.T, size int) *rig {
		want := payload(size)
		src := writeSource(t, want)
		ss := newServerSide(t, func(dir string) {
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
		ds := newDriveSide(t, nil)
		if err := ds.session.Mkdir(filepath.Dir(destPath)); err != nil {
			t.Fatal(err)
		}
		faulty := ds.proxy
		if onSource {
			faulty = ss.proxy
		}
		r := &rig{want: want, opts: faultOpts(), proxy: faulty, heal: func() {
			ss.proxy.Heal()
			ds.proxy.Heal()
		}}
		r.copy = func(ctx context.Context) error {
			_, err := ds.session.CopyIn(ctx, destPath, ss.session.Opener(destPath), r.opts)
			return err
		}
		r.got, r.entries = ds.readers(t)
		r.leftovers = func() []string { return append(tempsUnder(ss.root), ds.leftovers()...) }
		return r
	}}
}

func TestCopyFaultsDrive(t *testing.T) {
	for _, sc := range wireFaults(faultSize, faulttest.Down) {
		runScenario(t, driveDown, faultSize, sc)
	}
	runScenario(t, driveDown, faultSize, scenario{
		name: "truncated response",
		arm:  armProxy(faulttest.Truncate(faultSize / 2)),
		want: recovers,
	})
	// Big files go up in resumable chunks, small ones in a single request.
	for _, sc := range wireFaults(driveBigSize, faulttest.Up) {
		sc.name = "resumable, " + sc.name
		runScenario(t, driveUp, driveBigSize, sc)
	}
	for _, sc := range wireFaults(faultSize, faulttest.Up) {
		sc.name = "one request, " + sc.name
		runScenario(t, driveUp, faultSize, sc)
	}

	mid := int64(faultSize / 2)
	runScenario(t, across(true), faultSize, scenario{
		name: "source resets mid-copy",
		arm:  armProxy(faulttest.Fault{Dir: faulttest.Down, At: mid, Action: faulttest.Reset}),
		want: recovers,
	})
	runScenario(t, across(false), faultSize, scenario{
		name: "destination resets mid-copy",
		arm:  armProxy(faulttest.Fault{Dir: faulttest.Up, At: mid, Action: faulttest.Reset}),
		want: recovers,
	})
	runScenario(t, across(false), faultSize, scenario{
		name: "destination down past the retry window",
		arm:  armProxy(faulttest.Fault{Dir: faulttest.Up, At: mid, Action: faulttest.Outage}),
		want: failsCleanly,
	})
}

// Cancelling while a Drive upload stands still must return at once and
// leave nothing.
func TestCopyFaultsDriveCancelDuringStall(t *testing.T) {
	for _, size := range []int{faultSize, driveBigSize} {
		r, _ := driveUpRig(t, size)
		r.proxy.Arm(faulttest.Fault{Dir: faulttest.Up, At: int64(size / 2), Action: faulttest.Stall, For: time.Hour})

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- r.copy(ctx) }()
		select {
		case <-r.proxy.FiredCh():
		case <-time.After(5 * time.Second):
			cancel()
			t.Fatal("the stall never started")
		}
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("size %d: copy returned %v, want context.Canceled", size, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("size %d: the copy did not return within 1s of being cancelled", size)
		}
		r.proxy.Heal()
		r.assertNothing(t)
	}
}

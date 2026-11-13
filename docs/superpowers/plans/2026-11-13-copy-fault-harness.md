# Copy Fault Harness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prove, with simulated faults that run in CI, that every Go copy path either recovers byte for byte or fails cleanly, and fix what does not.

**Architecture:** A new test-support package `core/faulttest` holds a real TCP fault proxy and a faulty `vault.WriteVFS` wrapper. One scenario table in `webserver/server/copy_faults_test.go` runs each fault against each copy path and checks one of two declared outcomes under a hard deadline.

**Tech Stack:** Go 1.26, standard library only (`net`, `net/http/httptest`, `testing`). Existing packages: `core/xfer`, `core/vault`, `core/remote`, `core/drive`, `core/drive/drivetest`, `webserver/server`.

**Spec:** `docs/superpowers/specs/2026-11-13-copy-fault-harness-design.md`

## Global Constraints

- Go only. Do not touch Swift, Kotlin or web client code.
- No new module dependencies.
- Build tag for every `go` command in `core` and `webserver`: `-tags without_openssl`.
- All new tests pass under `-race` and finish in seconds: the whole of `copy_faults_test.go` in under 60 s.
- Every scenario asserts its fault fired. A scenario that passes without its fault firing is a test bug.
- A hang is a failure reported by the scenario's own deadline, never by the `go test` timeout.
- Decrypted content is never written to disk by product code. Test sources are plain files in `t.TempDir()`, which is fine.
- Never replace a working mechanism. A finding is fixed with the smallest change to the existing code.
- A change under `core/` is checked with `make core-test` **and** `make web-test`.
- Each task ends with an `aj-log.md` entry (what changed, why, what was and was not checked) and a commit ending with `Co-Authored-By: Claude Fable 5.2 <noreply@anthropic.com>`.
- The spec's `Down(d)` action is named `Outage` in code, because `Down` is also a direction.

## Review Focus

1. **A scenario passes vacuously** because the fault fired on other traffic or never fired. Expected: bytes before `Arm` do not count; the runner fails a scenario whose `Fired()` is false. Tests: Task 1 step 1 (`TestArmStartsCountingFromZero`), Task 4 runner.
2. **Odd sizes:** an empty file, and a file that is an exact multiple of the 4096-byte block. Expected: both recover or fail cleanly like any other. Test: Task 4 size sweep.
3. **A second fault during the recovery from the first.** Expected: still recovers while each stretch is inside the retry window. Test: Task 5 `reset twice`.
4. **Cancel while nothing is moving** (stall or blackhole). Expected: the copy returns within 1 s of cancel and leaves nothing. Test: Task 5 `cancel during stall`.
5. **The proxy leaks goroutines or connections**, making later tests flaky. Expected: `Close` returns only after every forwarder has stopped. Test: Task 1 `TestCloseStopsEverything`.

---

## File Structure

| File | Responsibility |
|------|----------------|
| `core/faulttest/proxy.go` (new) | TCP proxy and its faults |
| `core/faulttest/proxy_test.go` (new) | Proxy behaviour against a raw echo server |
| `core/faulttest/fs.go` (new) | Faulty `vault.WriteVFS` wrapper and faulty `xfer.Opener` |
| `core/faulttest/fs_test.go` (new) | FS behaviour over `vault.LocalFS` |
| `core/Makefile` (modify lines 25, 29, 32) | Add `./faulttest/...` to the test and vet package lists |
| `core/drive/drivetest/drivetest.go` (modify) | `PublicURL` so upload session links go through a proxy |
| `webserver/server/copy_faults_test.go` (new) | Runner, paths, scenario table |
| `webserver/server/copy_faults_http_test.go` (new) | Server-decrypt scenarios with a plain HTTP client |

---

### Task 1: `faulttest.Proxy`

**Files:**
- Create: `core/faulttest/proxy.go`, `core/faulttest/proxy_test.go`
- Modify: `core/Makefile:25,29,32` (append `./faulttest/...`)

**Interfaces:**
- Produces:

```go
package faulttest

type Dir int
const (
	Up   Dir = iota // client to target
	Down            // target to client
)

type Action int
const (
	Close     Action = iota // close both sides cleanly at the byte count
	Reset                   // close both sides with a TCP reset (SetLinger(0))
	Stall                   // stop forwarding for Fault.For, then continue
	Blackhole               // swallow bytes in both directions until Heal
	Outage                  // reset open connections and refuse new ones for Fault.For, or until Heal when For is 0
)

type Fault struct {
	Dir    Dir
	At     int64         // fires when this many bytes have passed in Dir since Arm
	Action Action
	For    time.Duration // Stall and Outage
}

// Truncate is a clean close of a response at byte at.
func Truncate(at int64) Fault // Fault{Dir: Down, At: at, Action: Close}

func NewProxy(t testing.TB, target string) *Proxy // target is host:port; t.Cleanup closes it
func (p *Proxy) Addr() string                     // 127.0.0.1:port
func (p *Proxy) URL() string                      // "http://" + Addr()
func (p *Proxy) SetTarget(target string)          // new connections go here
func (p *Proxy) Arm(f Fault)                      // zeroes both byte counters; one fault at a time; fires once
func (p *Proxy) Heal()                            // ends Stall, Blackhole, Outage
func (p *Proxy) Fired() bool
func (p *Proxy) FiredCh() <-chan struct{}         // closed when the armed fault fires; new channel per Arm
func (p *Proxy) Close()
```

Byte counters are shared by all connections. The bytes up to `At` are forwarded before the action; bytes after it follow the action.

- [ ] **Step 1: Write the failing tests** in `proxy_test.go`, against a raw TCP echo server started in the test (`net.Listen` + `io.Copy(conn, conn)`):

| Test | Assertions |
|------|------------|
| `TestForwardsUntouched` | 1 MiB written through the proxy comes back identical; `Fired()` is false |
| `TestCloseAtByte` | `Arm(Fault{Dir: Up, At: 1000, Action: Close})`; write 4000 bytes; client reads exactly 1000 echoed bytes then `io.EOF`; `Fired()` true |
| `TestResetAtByte` | same with `Reset`; the read error is not `io.EOF` (`errors.Is(err, syscall.ECONNRESET)`) |
| `TestTruncateResponse` | `Arm(Truncate(500))`; client receives exactly 500 bytes then `io.EOF` |
| `TestStallThenContinues` | `Stall` for 150 ms at byte 100 of 1000; all 1000 bytes arrive; elapsed ≥ 150 ms |
| `TestBlackholeUntilHeal` | after firing, a read with a 200 ms deadline times out; after `Heal()` a new connection echoes normally |
| `TestOutageRefusesThenHeals` | after firing, `net.Dial(Addr())` fails or the connection is closed before any echo; after `Heal()` echo works; with `For: 100ms` it heals by itself |
| `TestArmStartsCountingFromZero` | send 5000 bytes, then `Arm` at 1000: the fault fires 1000 bytes after `Arm`, not at once |
| `TestFiresOnce` | after a `Close` fault fires, a new connection passes 1 MiB untouched |
| `TestSetTarget` | new connections after `SetTarget` reach a second echo server that prefixes nothing but is distinguishable by its listener's accept count |
| `TestCloseStopsEverything` | open 20 connections, call `Close()`, then `runtime.NumGoroutine()` returns to its value before `NewProxy` within 2 s |

- [ ] **Step 2: Run them, expect a compile failure**

Run: `cd core && go test -tags without_openssl ./faulttest/`
Expected: FAIL, `undefined: NewProxy`.

- [ ] **Step 3: Implement `proxy.go`.** One accept loop; per connection two forwarder goroutines that read into a 32 KiB buffer and call one mutex-guarded `p.account(dir, n) (allow int, act *Fault)` that splits a buffer straddling `At`. `Close` closes the listener and every tracked connection and waits on a `sync.WaitGroup`.

- [ ] **Step 4: Run with the race detector**

Run: `cd core && go test -tags without_openssl -race -count=5 ./faulttest/`
Expected: `ok`, all five runs.

- [ ] **Step 5: Add `./faulttest/...` to the three package lists in `core/Makefile`, write the `aj-log.md` entry, commit**

```bash
git add core/faulttest/proxy.go core/faulttest/proxy_test.go core/Makefile aj-log.md
git commit -m "faulttest: a TCP proxy that cuts, resets, stalls and blackholes"
```

---

### Task 2: `faulttest.FS` and `FlakyOpener`

**Files:**
- Create: `core/faulttest/fs.go`, `core/faulttest/fs_test.go`

**Interfaces:**
- Consumes: `vault.WriteVFS`, `vault.NewLocalFS(root)`, `xfer.Opener`, `xfer.Source`.
- Produces:

```go
// FS wraps a WriteVFS. It embeds the interface, so the inner backend's
// optional capabilities (FileOpener, StaleTempRemover) are hidden on purpose:
// every read goes through ReadAt, where the faults are.
type FS struct{ vault.WriteVFS /* + unexported state */ }

func NewFS(inner vault.WriteVFS) *FS

type ReadFault struct {
	From  int64         // only reads at or after this offset
	Calls int           // fail this many matching calls (0 with For set = unlimited within For)
	For   time.Duration // fail matching calls for this long after the first one
	Short bool          // return (0, nil) instead of Err
	Err   error
}

func (f *FS) FailReads(rf ReadFault)
func (f *FS) FailWrites(times int, after int64, err error) // the next `times` temp files fail Write once `after` bytes are in
func (f *FS) FailClose(times int, err error)               // the next `times` temp files fail Close
func (f *FS) FailRename(times int, landed bool, err error) // landed: do the rename, then return err
func (f *FS) Fired() bool                                  // any fault returned its error at least once

// FlakyOpener applies rf to the ReadAt of every Source that open returns.
// The returned func reports whether the fault has fired.
func FlakyOpener(open xfer.Opener, rf ReadFault) (xfer.Opener, func() bool)
```

- [ ] **Step 1: Write the failing tests** in `fs_test.go`, over `vault.NewLocalFS(t.TempDir())` with a 10 000-byte file written through `WriteFile`:

| Test | Assertions |
|------|------------|
| `TestReadFaultCalls` | `FailReads{From: 4096, Calls: 2, Err: syscall.EIO}`: `ReadAt` at offset 0 succeeds; at 5000 fails twice with `EIO`, third time succeeds; `Fired()` true |
| `TestReadFaultFor` | `For: 100ms`: fails now, succeeds after 120 ms |
| `TestReadFaultShort` | `Short: true`: returns `0, nil` |
| `TestWriteFault` | `FailWrites(1, 3000, syscall.ENOSPC)`: writing 5000 bytes to a `CreateTemp` writer returns `ENOSPC` with `errors.Is`; the next temp file takes 5000 bytes |
| `TestCloseFault` | `FailClose(1, syscall.EIO)`: `Close` returns `EIO` once |
| `TestRenameFailed` | `FailRename(1, false, syscall.EIO)`: error returned, source still exists, destination does not |
| `TestRenameLanded` | `FailRename(1, true, syscall.ETIMEDOUT)`: error returned, destination exists, source gone |
| `TestFlakyOpener` | over `xfer.OpenFile(path)`: `Size()` unchanged; `ReadAt` fails per the fault; the fired func turns true |

- [ ] **Step 2: Run, expect a compile failure**

Run: `cd core && go test -tags without_openssl ./faulttest/`
Expected: FAIL, `undefined: NewFS`.

- [ ] **Step 3: Implement `fs.go`.** Mutex-guarded counters; `CreateTemp` returns a wrapper around the inner writer.

- [ ] **Step 4: Run**

Run: `cd core && go test -tags without_openssl -race ./faulttest/ && make -C .. core-test`
Expected: `ok`; `core-test` passes.

- [ ] **Step 5: `aj-log.md` entry, commit**

```bash
git add core/faulttest/fs.go core/faulttest/fs_test.go aj-log.md
git commit -m "faulttest: a filesystem wrapper that fails reads, writes and renames"
```

---

### Task 3: the fake Drive can sit behind a proxy

`drivetest` builds upload session links from its own address (`core/drive/drivetest/drivetest.go:441`), so a resumable upload would bypass the proxy.

**Files:**
- Modify: `core/drive/drivetest/drivetest.go` (`Server` struct, `Config()`, lines 174 and 441)
- Test: `core/drive/vaultfs_test.go`

**Interfaces:**
- Produces: `Server.PublicURL string`. When set, `Config()` and every link the server hands out (upload session `Location`, `thumbnailLink`) use it in place of `s.URL`.

- [ ] **Step 1: Write the failing test** `TestPublicURLCarriesUploads` in `core/drive/vaultfs_test.go`: start `drivetest.New()`, start a counting `httptest.Server` that reverse-proxies to it (`httputil.NewSingleHostReverseProxy`), set `srv.PublicURL` to the counting server's URL, create a vault with `newVault`, `Create` a 600 KiB file. Assert: the file reads back equal, and the counting server saw at least one request whose path starts with `/upload/session/`.

- [ ] **Step 2: Run, expect failure**

Run: `cd core && go test -tags without_openssl -run TestPublicURLCarriesUploads ./drive/`
Expected: FAIL, `srv.PublicURL undefined`.

- [ ] **Step 3: Add `PublicURL` and an unexported `base()` that returns it or `s.URL`; use `base()` in `Config()` and at the two link sites.**

- [ ] **Step 4: Run**

Run: `cd core && go test -tags without_openssl ./drive/...`
Expected: `ok`.

- [ ] **Step 5: `aj-log.md` entry, commit**

```bash
git add core/drive/drivetest/drivetest.go core/drive/vaultfs_test.go aj-log.md
git commit -m "drivetest: PublicURL, so a proxy can carry every request"
```

---

### Task 4: scenario runner, and the two filesystem paths

**Files:**
- Create: `webserver/server/copy_faults_test.go`

**Interfaces:**
- Consumes: Task 1 and 2 APIs; `(*vault.Session).CopyIn(ctx, plainPath, open, opts)`, `.Opener(plainPath)`, `.ReadFile`, `.Stat`, `.Mkdir`; `vault.Init(v, pw)`, `vault.Open(v, pw)`; `xfer.OpenFile`, `xfer.Options{Attempts, BaseDelay, MaxDelay}`.
- Produces (used by Tasks 5 to 7, same file or package):

```go
type outcome int
const (
	recovers outcome = iota
	failsCleanly
)

// rig is one prepared copy: source, destination and where faults go in.
type rig struct {
	want      []byte                               // the source bytes
	copy      func(ctx context.Context) error      // runs the copy with faultOpts()
	got       func() ([]byte, error)               // reads the destination back through a clean, second session
	leftovers func() []string                      // temp files at the destination and on the server
	proxy     *faulttest.Proxy                     // nil on filesystem paths
	fs        *faulttest.FS                        // nil on network paths
	srcFired  func() bool                          // FlakyOpener's report, or nil
	heal      func()                               // clears every fault
}

type copyPath struct {
	name  string
	setup func(t *testing.T, size int) *rig
}

type scenario struct {
	name     string
	arm      func(t *testing.T, r *rig) // arms exactly one fault
	want     outcome
	wantErr  error         // optional: errors.Is target for failsCleanly
	deadline time.Duration // 0 means 10 s
	skip     string        // reason; names the later piece
}

func runScenario(t *testing.T, p copyPath, size int, sc scenario)
func faultOpts() xfer.Options // Attempts: 8, BaseDelay: time.Millisecond, MaxDelay: 20 * time.Millisecond
func payload(size int) []byte // deterministic: math/rand/v2 PCG seeded (1, 2)
const faultSize = 3<<20 + 123 // several blocks plus a partial one
const destPath = "in/file.bin" // setup makes "in" before any fault is armed
```

`runScenario` does, in order: `t.Skip(sc.skip)` if set; `setup`; `arm`; run `copy` in a goroutine and wait for it or the deadline (on deadline: `t.Fatalf("%s/%s: hung past %v", ...)`); assert a fault fired (`proxy.Fired()`, `fs.Fired()` or `srcFired()`); then

- `recovers`: `err == nil`, `sha256(got) == sha256(want)`, `leftovers()` empty.
- `failsCleanly`: `err != nil` (and `errors.Is(err, wantErr)` when set), `got()` reports not found (`vault.ErrNotFound`), `leftovers()` empty; then `heal()`, run `copy` again: `err == nil` and the bytes match.

Leftovers on a local vault: walk the vault directory for names starting with `.imagethat-` (the upload temp prefix is `.imagethat-upload-`, `core/vault/write.go:29`; the server's own temp files share the shorter prefix, as the existing check at `raw_write_test.go:92` assumes).

- [ ] **Step 1: Write the runner, the two paths and their scenarios.**

Paths:
- `localIn`: plain file in `t.TempDir()` → vault created with `vault.Init` + `vault.Open` over `faulttest.NewFS(vault.NewLocalFS(dir))`. `got` opens the directory with `vault.OpenLocal`.
- `mountIn`: source is `faulttest.FlakyOpener(xfer.OpenFile(src), fault)` → clean local vault.

Scenarios (function `TestCopyFaultsFilesystem`, one `t.Run(path/scenario)` each):

| Path | Scenario | Fault | Want |
|------|----------|-------|------|
| mountIn | read error early, middle, last byte | `ReadFault{From: 0 / size/2 / size-1, Calls: 3, Err: syscall.EIO}` | recovers |
| mountIn | read times out for a while | `ReadFault{From: size/2, For: 60 * time.Millisecond, Err: syscall.ETIMEDOUT}` | recovers |
| mountIn | short reads | `ReadFault{From: size/2, Calls: 3, Short: true}` | recovers |
| mountIn | outage past the retry window | `ReadFault{From: size/2, For: time.Hour, Err: syscall.EIO}`; `heal` replaces the opener's fault with none | failsCleanly |
| mountIn | source shrinks mid-copy | an opener written in the test that truncates the file by 1000 bytes on its second open, plus one `EIO` to force that reopen | failsCleanly, `xfer.ErrSourceChanged` (no re-run check: `heal` restores the file) |
| localIn | write fails early, middle, late | `FailWrites(2, 4096 / size/2 / size-1, syscall.EIO)` | recovers |
| localIn | close fails | `FailClose(2, syscall.EIO)` | recovers |
| localIn | rename fails | `FailRename(2, false, syscall.EIO)` | recovers |
| localIn | commit landed, reply lost | `FailRename(1, true, syscall.ETIMEDOUT)` | recovers, and the vault's `in/` lists exactly one entry |
| localIn | disk full | `FailWrites(1000, size/2, syscall.ENOSPC)` | failsCleanly, `syscall.ENOSPC`, and `copy` returned in under 1 s (no retries) |
| localIn | write fails for good | `FailWrites(1000, size/2, syscall.EIO)` | failsCleanly |

Size sweep (`TestCopyFaultsOddSizes`): sizes `0`, `4096`, `4096*3`, `1`; on `localIn` with `FailRename(1, false, syscall.EIO)` and on `mountIn` with `ReadFault{From: 0, Calls: 1, Err: syscall.EIO}`. Want: recovers. For size 0 the read fault cannot fire on a read; arm it and accept `Fired()` false for that one case with a comment saying why.

- [ ] **Step 2: Run**

Run: `cd webserver && go test -tags without_openssl -race -run 'TestCopyFaults' ./server/`
Expected: `ok`, or named failures.

- [ ] **Step 3: Handle findings.** For each failing scenario decide which it is:
  - The runner or the fault is wrong: fix the test.
  - Today's promise is broken: fix the product code with the smallest change, in its own commit, with the scenario as its test and an `aj-log.md` entry naming the cause.
  - The fix would need a protocol change or a new subsystem: set `skip` with the reason and record it in `aj-log.md` for the user.

- [ ] **Step 4: Run the full suites**

Run: `make core-test && make web-test`
Expected: both pass.

- [ ] **Step 5: `aj-log.md` entry (scenarios, results, findings), commit**

```bash
git add webserver/server/copy_faults_test.go aj-log.md
git commit -m "Copy fault table: filesystem faults on local and mounted sources"
```

---

### Task 5: server paths through the proxy, and a server restart

**Files:**
- Modify: `webserver/server/copy_faults_test.go`
- Possibly modify: `core/remote/write.go`, `core/remote/client.go` (expected finding below)

**Interfaces:**
- Consumes: `newClientDecryptServer(t) (*Server, string)` (`access_test.go:18`), `testAccessPassword`, `fixturePassword`, `newClient(t, srv)` with `.login(pw)` and `.vaultID()`, `httptest.NewServer(srv)`, `remote.New(url, remote.Options{Timeout: 300 * time.Millisecond})`, `(*remote.Client).Login`, `.OpenVault(ctx, id, pw)`, `openLocal(t, srv)` (`raw_write_test.go:20`).
- Produces: `serverRig(t, size) (r *rig, srv *Server, root string, ts *httptest.Server)` shared by `serverUp` and `serverDown`; `restartServer(t, r, root) ` for the restart scenario.

Paths:
- `serverUp`: plain file → remote session opened through `proxy.URL()`. `got` reads through `openLocal`. `leftovers` walks `root/myvault`.
- `serverDown`: the file is first written with `openLocal(...).Create`, then copied from `remoteSession.Opener(destPath)` into a clean local vault.

Login, `OpenVault` and `Mkdir("in")` happen in `setup`, before `Arm`.

- [ ] **Step 1: Add the paths and `TestCopyFaultsServer`.** Offsets are byte counts in the fault's direction; `early = 8 << 10`, `mid = size / 2`, `late = size - 1`.

| Path | Scenario | Fault | Want |
|------|----------|-------|------|
| serverDown | close / reset, at early, mid, late | `{Down, off, Close}` / `{Down, off, Reset}` | recovers |
| serverDown | truncated response | `Truncate(mid)` | recovers |
| serverDown | stall under the timeout | `{Down, mid, Stall, For: 100ms}` | recovers |
| serverDown | blackhole | `{Down, mid, Blackhole}`; a goroutine calls `Heal()` 500 ms after `FiredCh()` closes | recovers |
| serverDown | short outage | `{Down, mid, Outage, For: 60ms}` | recovers |
| serverDown | outage past the retry window | `{Down, mid, Outage}` (no `For`) | failsCleanly |
| serverUp | close / reset, at early, mid, late | `{Up, off, Close}` / `{Up, off, Reset}` | recovers |
| serverUp | stall under the timeout | `{Up, mid, Stall, For: 100ms}` | recovers |
| serverUp | blackhole | `{Up, mid, Blackhole}`, healed 500 ms after firing | recovers |
| serverUp | short outage | `{Up, mid, Outage, For: 60ms}` | recovers |
| serverUp | outage past the retry window | `{Up, mid, Outage}` | failsCleanly |
| serverUp | commit landed, reply lost | not a proxy fault, because a byte count cannot pick out one small response reliably: build the client with `remote.Options{Transport: rt}` where `rt` wraps `http.DefaultTransport`, lets the first `POST .../raw/rename` reach the server, then closes the response body and returns `io.ErrUnexpectedEOF`. Set `rig.srcFired` to report that it happened | recovers; `in/` lists exactly one entry |
| serverUp | reset twice | `{Up, mid, Reset}`, and on `FiredCh()` arm `{Up, mid, Reset}` again | recovers |
| serverUp | cancel during stall | `{Up, mid, Stall, For: time.Hour}`; cancel the context on `FiredCh()` | `errors.Is(err, context.Canceled)` within 1 s, nothing at the destination, no leftovers (write this one outside `runScenario`) |
| serverUp | server restart | `{Up, mid, Outage}`; on `FiredCh()`: `ts.Close()`, `srv.Close()`, build a new `Server` over the same `root` with the same config, start a new `httptest.Server`, `proxy.SetTarget`, `Heal()` | recovers; any leftover must start with the upload temp prefix and there is at most one |

For the restart scenario `newClientDecryptServer` copies the fixture into a fresh root each call, so add `reopenClientDecryptServer(t, root string) *Server` next to the scenario: the same `Config` as `access_test.go:25-30` with `Root: root`, without copying the fixture.

- [ ] **Step 2: Run**

Run: `cd webserver && go test -tags without_openssl -race -run 'TestCopyFaultsServer' ./server/`
Expected: `ok`, or named failures.

- [ ] **Step 3: Expected finding, `serverUp/blackhole` hangs.** `core/remote/client.go:115` builds the `stream` client with no timeout, and `rawUpload.Close` (`core/remote/write.go:160`) waits for the server's answer with no bound. If the scenario hangs, fix it in `core/remote/write.go` only:
  - `CreateTemp` builds the request with a cancellable context.
  - `rawUpload` gets an idle timer of the client's `Timeout` (store `opts.Timeout` on `Client` as `idle time.Duration`). The timer is reset after every `Write` that returns, keeps running during `Close`, and on firing cancels the request context.
  - On firing, `Close` and `Write` return an error wrapping `context.DeadlineExceeded` (retryable in `xfer.Retryable`), so `CopyIn` restarts the file.
  - Unit test in `core/remote/client_test.go`, `TestStalledUploadTimesOut`: an `httptest.Server` whose handler reads the body and then blocks until the test ends; client `Timeout: 100ms`; write 1 KiB to `CreateTemp`'s writer and `Close`. Assert `Close` returns within 1 s and `errors.Is(err, context.DeadlineExceeded)`. A second case writes 1 KiB every 30 ms for 300 ms to a handler that answers 200: `Close` returns `nil` (a slow but moving upload is not cut).

  Handle every other failure by the Task 4 step 3 rules.

- [ ] **Step 4: Run the full suites**

Run: `make core-test && make web-test`
Expected: both pass.

- [ ] **Step 5: `aj-log.md` entry, commit (a product fix is its own commit before the test commit)**

```bash
git add webserver/server/copy_faults_test.go aj-log.md
git commit -m "Copy fault table: server uploads and downloads through a fault proxy"
```

---

### Task 6: Drive paths, and vault to vault

**Files:**
- Modify: `webserver/server/copy_faults_test.go`

**Interfaces:**
- Consumes: `drivetest.New()`, `Server.PublicURL` (Task 3), `Server.Config()`, `Server.AddFolder("root", "Vault")`, `Server.Children(parent) []string`, `drive.New(cfg)`, `drive.InitVault(c, rootID, pw)`, `drive.OpenVault(c, rootID, pw, nil)`; `serverRig` from Task 5.
- Produces: paths `driveUp`, `driveDown`, `across`.

Setup: `d := drivetest.New()`; `proxy := faulttest.NewProxy(t, d.Listener.Addr().String())`; `d.PublicURL = proxy.URL()`; `cfg := d.Config(); cfg.HTTPClient = &http.Client{Timeout: 300 * time.Millisecond}`. `got` reads through a second client built from a `Config` with `APIBase` set to `d.URL` (no proxy). `leftovers`: names under the vault's folders in `d.Children` that start with the upload temp prefix; walk folders with `d.Lookup`.

- [ ] **Step 1: Add the paths and `TestCopyFaultsDrive`.** The same rows as Task 5 for `serverDown` → `driveDown` and `serverUp` → `driveUp`, except: no server restart, and no cancel-during-stall. Add for `across` (source: client-decrypt server vault through proxy A; destination: Drive vault through proxy B):

| Scenario | Fault | Want |
|----------|-------|------|
| source resets mid-copy | proxy A `{Down, mid, Reset}` | recovers |
| destination resets mid-copy | proxy B `{Up, mid, Reset}` | recovers |
| destination out past the retry window | proxy B `{Up, mid, Outage}` | failsCleanly |

`rig.proxy` is the proxy that carries the fault; `heal` heals both.

- [ ] **Step 2: Run**

Run: `cd webserver && go test -tags without_openssl -race -run 'TestCopyFaultsDrive' ./server/`
Expected: `ok`, or named failures.

- [ ] **Step 3: Handle findings by the Task 4 step 3 rules.** Note for the log: `core/drive/upload.go` already resumes an interrupted upload session (`received()`, line 219). Record whether `driveUp` resets resume or restart, since piece 3 (resumable uploads) depends on it.

- [ ] **Step 4: Run the full suites**

Run: `make core-test && make web-test`
Expected: both pass.

- [ ] **Step 5: `aj-log.md` entry, commit**

```bash
git add webserver/server/copy_faults_test.go aj-log.md
git commit -m "Copy fault table: Google Drive, and vault to vault"
```

---

### Task 7: server-decrypt mode at the HTTP level

The Go client only speaks client-decrypt. These scenarios check the server's side of the promise with a plain `http.Client` through the proxy.

**Files:**
- Create: `webserver/server/copy_faults_http_test.go`

**Interfaces:**
- Consumes: `newTestServer(t)`, routes `POST /api/vaults/{id}/unlock`, `POST /api/vaults/{id}/upload`, `GET /api/vaults/{id}/file` (`handlers_fs.go:41-46`). Read `handleUpload` and the existing upload and file tests in `server_test.go` for the exact request shapes and copy them.

- [ ] **Step 1: Write `TestServerDecryptUploadCutLeavesNothing`.** Unlock through the proxy with a cookie-jar client; arm `{Up, size/2, Reset}`; `POST` the upload of `payload(faultSize)`. Assert: the request fails; `openLocal`-style `vault.OpenLocal(root/myvault)` has no file at the destination; no file under `root/myvault` starts with the upload temp prefix; no file in the server's temp location for uploads remains (find it in `handleUpload`). Then the same upload again with no fault returns 200 and the file reads back equal.

- [ ] **Step 2: Write `TestServerDecryptDownloadResumes`.** Put `payload(faultSize)` in the vault with a local session before the server starts. Arm `Truncate(size/2)`; `GET` the file and read `n` bytes until the error or EOF (`n < size`). Then `GET` with `Range: bytes=n-`. Assert: status 206, and first part + second part equals the payload by SHA-256.

- [ ] **Step 3: Run**

Run: `cd webserver && go test -tags without_openssl -race -run 'TestServerDecrypt(UploadCut|DownloadResumes)' ./server/`
Expected: `ok`, or a finding handled by the Task 4 step 3 rules.

- [ ] **Step 4: `aj-log.md` entry, commit**

```bash
git add webserver/server/copy_faults_http_test.go aj-log.md
git commit -m "Copy fault table: server-decrypt uploads and ranged downloads"
```

---

### Task 8: scenarios for the later pieces, and the final check

**Files:**
- Modify: `webserver/server/copy_faults_test.go`

- [ ] **Step 1: Add three skipped scenarios** in `TestCopyFaultsLater`, each with a real body and `skip` set:

| Scenario | Body | `skip` |
|----------|------|--------|
| a flipped byte is caught | `serverUp`; after a clean copy, flip one byte in the middle of the destination's ciphertext file on disk; a "verify" step must report a mismatch. Until piece 2 there is no verify call, so the body ends at `t.Fatal("no verification API yet")` | `"piece 2: content verification"` |
| same name and size, other content, is not skipped | destination already holds a different payload of the same size at `destPath`; a re-run must not report success | `"piece 2: content verification"` |
| an upload cut at 90% does not resend the first 90% | `serverUp` with `{Up, size*9/10, Reset}`; assert the proxy carried fewer than `size*12/10` bytes up in total | `"piece 3: resumable uploads"` |

This needs `func (p *Proxy) Bytes(d Dir) int64` (total since `NewProxy`): add it to `core/faulttest/proxy.go` with one assertion in `TestForwardsUntouched`.

- [ ] **Step 2: Run everything three times under the race detector**

Run: `cd core && go test -tags without_openssl -race -count=3 ./faulttest/ && cd ../webserver && go test -tags without_openssl -race -count=3 -run 'TestCopyFaults|TestServerDecrypt(UploadCut|DownloadResumes)' ./server/`
Expected: `ok` for both, total under 3 minutes. A scenario that fails once in three is flaky: fix its timing by waiting on `FiredCh()`, not by adding sleeps.

- [ ] **Step 3: Run every suite the change can reach**

Run: `make core-test && make web-test && make golib-test`
Expected: all pass.

- [ ] **Step 4: Final `aj-log.md` entry and commit.** The entry lists: scenario counts per path, every finding with its fix or its skip reason, the three later-piece scenarios, what was not checked (Swift paths, real hardware).

```bash
git add core/faulttest/proxy.go core/faulttest/proxy_test.go webserver/server/copy_faults_test.go aj-log.md
git commit -m "Copy fault table: scenarios waiting on verification and resumable uploads"
```

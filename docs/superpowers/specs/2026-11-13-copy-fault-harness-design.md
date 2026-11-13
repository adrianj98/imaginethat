# Copy fault harness: design

Date: 2026-11-13. Piece 1 of 4 in the copy fault-tolerance work.

## Why

The user wants copies to be fault tolerant and wants that proven. The engine
already retries reads for about five minutes, restarts a file after a failed
write, skips files already at the destination and never leaves a partial file
(aj-log, 2026-11-09). None of that has been run against a real outage: the
existing tests inject errors at the Go interface level, never on a socket.

The four pieces, in the agreed order:

1. **Fault harness and tests** (this spec).
2. Content verification (checksum, not only size).
3. Resumable uploads.
4. Batches survive quit, crash and sleep (Mac).

## Decisions made with the user

- Faults are simulated and run in CI. No real hardware, no random soak run.
- Real TCP proxy plus a faulty filesystem wrapper, not HTTP-level fakes.
- Go only. The Mac's Swift-only copy paths (`TransferRetry` in
  `AppState.copyFile`: plain folder destinations, server-decrypt uploads from
  the Mac) are out of scope.
- A bug in today's behaviour is fixed in this piece. A scenario that needs
  piece 2, 3 or 4 is written now and skipped, naming the piece.

## Goal and success criteria

After every injected fault, on every Go copy path, a copy ends in one of two
states, and the test table says which:

- **Recovers:** the destination decrypts to the source's exact bytes
  (SHA-256), and no temp file is left at the destination or on the server.
- **Fails cleanly:** an error comes back before the scenario's deadline,
  nothing exists at the destination path, no temp file is left, and after the
  fault is healed a re-run recovers.

A hang is a failure: every scenario runs under a hard deadline and reports
which scenario timed out.

The table runs in seconds as part of `make core-test` and `make web-test`.

## Components

### `core/faulttest` (new package)

A normal package that only tests import, like `core/drive/drivetest`. It has
its own unit tests and is added to the package lists in `core/Makefile`.

**`Proxy`**: a TCP proxy on `127.0.0.1:0` that forwards to one target address.

- `NewProxy(t, target) *Proxy`, `URL()`/`Addr()`, closed by `t.Cleanup`.
- `Arm(Fault)`: byte counters start at zero when a fault is armed, so traffic
  before the copy (login, listing) does not count. One fault is armed at a
  time; it fires once.
- `Heal()`: ends a blackhole, a stall or a down period.
- `Fired() bool`: the scenario asserts the fault really happened.

A `Fault` has a direction (`Up` = client to target, `Down` = target to
client), a byte count to fire at, and one action:

| Action | Effect |
|--------|--------|
| `Close` | Both sides closed cleanly at the byte count. |
| `Reset` | Closed with a TCP reset (`SetLinger(0)`). |
| `Stall(d)` | Forwarding stops for `d`, connection stays open, then continues. |
| `Blackhole` | Bytes are swallowed and nothing is forwarded until `Heal`. No error reaches either side. |
| `Truncate` | Response bytes stop at the count and the connection closes cleanly, so the body is short of its Content-Length. |
| `Down(d)` | Open connections are reset and new ones refused for `d` or until `Heal`. Stands for Wi-Fi off or a server restart. |

**`FS`**: wraps a `vault.WriteVFS` (and offers an `xfer.Opener` wrapper for
plain source files) to stand for a network mount.

- Read faults: fail `ReadAt` at or after an offset, for N calls or for a
  duration; short read with no error.
- Write faults: fail `Write` after N bytes; fail `Close`.
- Rename faults: fail and do nothing; or do the rename and return an error
  (the reply was lost).
- The error is chosen by the scenario: `EIO`, `ETIMEDOUT`, `ENOSPC`.

The `writeFaults`/`dyingWriter` helpers in `core/vault/copy_test.go` stay as
they are; they test `CopyIn` in its own package and cannot import a package
that imports `vault` without a cycle.

### `webserver/server/copy_faults_test.go` (new)

The scenario table lives in the webserver module because it is the only one
that sees the real server, `core/remote`, `core/drive/drivetest` and local
vaults together.

A scenario is `{name, path, fault, outcome, deadline, skip}`. A path is a
small struct with `setup` (build source and destination, return a `copy`
func, a `verify` func and a `leftovers` func). Retry delays are set to
milliseconds through `xfer.Options` and the remote client's `Timeout` option.

## Paths

| Path | Source | Destination | Faults through |
|------|--------|-------------|----------------|
| local in | plain file | local vault | `FS` on the destination |
| mount in | plain file behind `FS` opener | local vault | `FS` on the source |
| server up | plain file | client-decrypt server vault (`remote.RawFS`) | `Proxy` |
| server down | client-decrypt server vault | local vault | `Proxy` |
| drive up | plain file | Drive vault on `drivetest` | `Proxy` |
| drive down | Drive vault on `drivetest` | local vault | `Proxy` |
| across | client-decrypt server vault | Drive vault | `Proxy` on each side in turn |
| server-decrypt HTTP | `POST /upload` and ranged media `GET` with a plain HTTP client | server's own session | `Proxy` |

Correction to what was said in chat: the Go client (`core/remote`) only
speaks client-decrypt. Server-decrypt mode is covered at the HTTP level: an
upload cut mid-body must leave nothing in the vault and no server temp file,
and a ranged download must be resumable from the byte it stopped at.

**Server restart:** for the server paths, one scenario arms `Down`, closes
the `httptest` server, starts a new `Server` on the same vault directory
(new sessions, the old one's in-flight temp file orphaned), points the proxy
at it and heals. Expected: recovers, and the orphaned temp file is the one
case allowed to remain, since stale-temp cleanup removes it after 24 h. The
scenario asserts it carries the upload temp prefix.

## Scenarios

Each fault is tried early (first block), in the middle, and on the last
bytes or the commit, on every path it applies to. Expected outcomes below
are what today's design promises; a scenario that does not meet its outcome
is a finding.

| Fault | Expected |
|-------|----------|
| `Close`, `Reset` mid-download | recovers (resume from offset) |
| `Close`, `Reset` mid-upload | recovers (file restarts) |
| `Truncate` on a download | recovers |
| `Stall` shorter than the request timeout | recovers |
| `Blackhole` on a download | recovers after the request timeout |
| `Blackhole` on an upload | recovers after a timeout; **expected finding**: the streamed upload client has no timeout (`core/remote/client.go:115`) |
| `Down` shorter than the retry window | recovers |
| `Down` longer than the retry window | fails cleanly, then recovers on re-run |
| Commit landed, reply lost (`FS` rename, or `Reset` after the upload's last byte) | recovers, counted as done, file present once |
| `ENOSPC` on write | fails cleanly at once, no retries |
| `EIO` on read N times, then fine | recovers |
| Source shrinks or grows mid-copy | fails cleanly (`ErrSourceChanged`) |
| Cancel during a `Stall` | returns promptly, nothing left |
| Server restart mid-upload | recovers |

Written now and skipped for later pieces:

- A byte flipped in flight with the size unchanged is caught (piece 2).
- A re-run does not skip a same-name, same-size file with other content
  (piece 2).
- An upload cut at 90% resumes without resending the first 90% (piece 3).

## Findings

- A scenario failing against today's promise is a bug. It is fixed in this
  piece with the smallest change to the existing code, and the scenario is
  its test. Each fix is its own commit and aj-log entry.
- If a fix turns out to need a protocol change or a new subsystem, it stops
  being part of this piece: the scenario is skipped with a note and the user
  is asked.

## Testing the harness itself

`core/faulttest` unit tests: each proxy action observed with a raw TCP echo
server (bytes delivered before the fault, the error the client sees, `Heal`
restores flow, `Fired`), and each `FS` fault over `vault.LocalFS`.

## Out of scope

- Swift copy paths and the Copies window.
- Real SMB/NFS shares, real Wi-Fi drops.
- Random or soak testing.
- Any change to how copies work beyond fixing what the table finds.

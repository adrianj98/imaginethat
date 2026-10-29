# imagethat-web

A standalone Go webserver that browses [gocryptfs](https://github.com/rfjakob/gocryptfs)
vaults from a browser: file browser, image viewer, and range-streamed video.

It is the same job the macOS ImageThat app does, reachable from a phone,
tablet, or any other machine. It is a separate Go module and shares no build
state with the app — `gocryptfs-lib/` and the Swift sources are untouched.

```
imagethat-web -root ~/Vaults
```

Then open <http://127.0.0.1:8347> and unlock a vault with its password.

## Where the crypto lives

The gocryptfs engine is `core/vault`, a Go package shared by this server, the
Android app (through `core/mobile` and gomobile) and the macOS app (through
`gocryptfs-lib`, a thin cgo shim over the same package). This module holds only
the HTTP layer: sessions, cookies, rate limiting, range serving and the
encrypted thumbnail cache.

`core/vault` started as a port of the original cgo library; compared with that
library it fixes a few things that matter for a server:

| Behaviour | original `gocryptfs-lib` | `core/vault` |
|---|---|---|
| Random access | re-reads and re-parses the file header on every range request | parsed once at `Open` |
| Writing a file | whole plaintext buffered in memory | streamed block by block into a temp file |
| Path resolution failure | falls back to the plaintext path, surfacing as a bogus "not found" | returns the real error |
| `..` in a path | n/a | rejected outright |
| Logging | unconditionally appends vault-relative plaintext paths to `/tmp/gocryptfs.log` | no such log |

## Building

`gocryptfs` keeps its crypto in `internal/` packages, which Go will not let
another module import. `core/scripts/sync-gocryptfs.sh` copies the nine-package
transitive closure out of the module cache and rewrites the import paths into
`core/internal/gocryptfs/`. That directory is generated and gitignored; `make`
regenerates it automatically when missing. This module depends on `../core`
through a `replace` directive in `go.mod`.

```
make sync     # copy the gocryptfs internal packages (implied by other targets)
make web      # build the browser UI into server/dist (needs Node)
make build    # build bin/imagethat-web
make test     # Go tests
make run ROOT=~/Vaults
```

The UI is compiled into the binary with `go:embed`, so `make web` must run
before `make build` for the bundle to be included. Without it the server still
runs and the JSON API works; it just serves a page saying the UI is not built.

From the repository root, `make web-all` does both steps.

## Releasing

Releases are cut only by pushing a `vX.Y.Z` tag; merges to `main` do not
release anything:

```
git tag v0.1.0 && git push origin v0.1.0
```

`.github/workflows/release.yml` takes the version from the tag and publishes
one GitHub release on it, holding the webserver binaries and `SHA256SUMS`.
Tags that are not exactly `vX.Y.Z` are rejected.

The webserver job builds the UI once, then cross-compiles linux and darwin on amd64/arm64
from a single runner, since everything is pure Go with `CGO_ENABLED=0`. Windows
is not built: gocryptfs' `syscallcompat` is POSIX-only. Tests are not re-run
here — `webserver-ci.yml` covers the same commit.

## Security model

The threat this server is built around is that decrypted vault content should
not outlive the moment it is being viewed.

- **Keys live in memory only.** Unlocking derives the cipher state and wipes
  the master key immediately. Nothing about a password or key is written to
  disk, so killing the process leaves nothing to recover.
- **Sessions are per browser.** A session cookie (`HttpOnly`, `SameSite=Strict`)
  identifies the browser; which vaults it has unlocked is server-side state.
  One browser unlocking a vault does not unlock it for anyone else.
- **Idle vaults lock themselves** after `-idle-timeout` (default 30 minutes).
- **Cached thumbnails are encrypted.** A preview is derived from vault content
  and is just as sensitive as the original, so the in-memory LRU stores it
  sealed with AES-256-GCM under a key bound to that unlocked vault. Locking
  wipes the key, which makes every cached entry unreadable at that instant.
  Nothing is cached on disk.
- **Responses carrying plaintext are `no-store`,** so the browser does not
  write decrypted content into its own on-disk cache. `Last-Modified` is
  suppressed for the same reason.
- **Paths are validated once, centrally.** Every path from a query parameter
  goes through `vault.CleanPath`, which rejects `..` rather than resolving it.
- **The API never discloses filesystem paths** — vaults are addressed by an
  opaque ID derived from a hash of their location.
- **Unlock attempts are rate limited** per vault and client.
- **Non-loopback binds require TLS.** The server refuses to start on a public
  address without `-tls-cert`/`-tls-key` unless you pass `-insecure`.

What this does *not* defend against: anyone who can read the server process's
memory while a vault is unlocked, and anyone who can reach the port with a
valid session cookie. Run it on loopback, or behind TLS on a network you trust.

## Flags

| Flag | Default | Meaning |
|---|---|---|
| `-root` | `.` | Directory scanned for vaults |
| `-addr` | `127.0.0.1:8347` | Listen address |
| `-scan-depth` | `3` | Levels below `-root` to search |
| `-idle-timeout` | `30m` | Lock vaults after this much inactivity (`0` disables) |
| `-thumb-cache` | `256` | Per-vault encrypted thumbnail budget, in MiB |
| `-tls-cert`, `-tls-key` | — | Serve HTTPS |
| `-insecure` | `false` | Permit a non-loopback bind without TLS |
| `-version` | | Print the version and exit |

## HTTP API

All endpoints are same-origin and need the session cookie. Mutating requests
are additionally checked against the `Origin` header.

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/api/vaults` | Discovered vaults and their lock state |
| `POST` | `/api/vaults/{id}/unlock` | `{"password": "..."}` |
| `POST` | `/api/vaults/{id}/lock` | Lock and wipe keys |
| `GET` | `/api/vaults/{id}/list?path=` | Directory listing |
| `GET` | `/api/vaults/{id}/file?path=` | File bytes; supports `Range` |
| `GET` | `/api/vaults/{id}/thumb?path=&size=` | JPEG preview, `204` if undecodable |
| `POST` | `/api/vaults/{id}/upload?path=&name=` | Raw body streamed into the vault |
| `POST` | `/api/vaults/{id}/mkdir` | `{"path": "..."}` |
| `POST` | `/api/vaults/{id}/move` | `{"from": "...", "to": "..."}` |
| `DELETE` | `/api/vaults/{id}/entry?path=` | Delete a file or directory tree |
| `POST` | `/api/logout` | End the session, locking every vault |
| `GET`, `HEAD` | `/api/vaults/{id}/raw/list?path=` | Ciphertext directory listing (encrypted names, ciphertext sizes) |
| `GET`, `HEAD` | `/api/vaults/{id}/raw/file?path=` | Ciphertext file bytes as on disk; supports `Range`; `path=gocryptfs.conf` is the vault config |

### Decrypted or encrypted

The server can send a vault either way. The endpoints above `raw/` decrypt on
the server and send plaintext; browsers use them, and every write goes through
them. The `raw/` endpoints send the vault's gocryptfs files as they are on disk,
so a client that holds the password (`core/remote`, used by the macOS and
Android apps) decrypts locally: nothing on the wire is plaintext, and anything
the client caches is ciphertext. They need the same unlocked session as the
rest, because ciphertext plus `gocryptfs.conf` would otherwise allow offline
password guessing. Paths are ciphertext paths; `..` and symlinks leading out of
the vault are refused.

Video seeking works because `vault.File` implements `io.ReadSeeker`, letting
`http.ServeContent` answer range requests directly out of the encrypted file.

## Testing

The test vault in `core/vault/testdata/vault` was produced by the real gocryptfs
v2.6.1 binary, so the read path is checked against genuine upstream ciphertext
rather than against this package's own output.
`TestInteropWithGocryptfsBinary` closes the loop in the other direction: it
writes with this package, mounts the result with the gocryptfs binary, and
requires it to agree on every byte. That test skips automatically when the
binary or FUSE is unavailable.

```
make test          # everything
make test-short    # skips the tests needing FUSE
```

## Limitations

- Local vaults only. The `vault.VFS` interface in `core` leaves room for a
  Drive-backed backend like the app's, but none is implemented here yet.
- HEIC/HEIF and camera raw files are listed and downloadable but get no
  server-side thumbnail — Go has no pure-Go decoder for them. Video thumbnails
  are not generated either.
- `.mkv` and `.avi` stream correctly but most browsers cannot play them; the
  viewer says so and offers a download.

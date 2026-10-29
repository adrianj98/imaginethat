# aj-log

## 2026-10-28 — Large-video tests over a slow gocryptfs drive (Mac app)

- **Tests/SlowVideoTest.swift (new):** builds a ~544 MB H.264 video (moov atom at
  the end) with ffmpeg, encrypts it into a fresh gocryptfs vault, and serves the
  vault through the Go library's VFS callbacks via a `SlowDrive` that adds
  per-read latency and a bandwidth cap. Covers: byte-exact range reads, duration
  and track loading, frames at 1s and near the end, reading less than a third of
  the file for metadata plus thumbnails, playback, seeking to 75%, and reads
  stopping once the player is closed.
  *Why:* the request was to prove that large encrypted videos on slow network
  drives play.
- **Tests/IntegrationTest.swift:** calls the new section. Set
  `IMAGETHAT_SKIP_SLOW_VIDEO=1` to skip it; `IMAGETHAT_SLOW_LATENCY_MS`,
  `IMAGETHAT_SLOW_MBPS` and `IMAGETHAT_SLOW_VIDEO_SECONDS` tune it.
- **EncryptedResourceLoader.swift:** added the `EncryptedRangeReading` protocol
  (GocryptfsBridge conforms), so tests can pass in a throttled reader.
  *Bug fix:* when a player item is released, AVFoundation drops in-flight loading
  requests without cancelling them, so the background loop kept reading to EOF
  over the network (measured at ~3.8 MB/s for 20+ s after closing). The loader
  now sets a flag in `deinit` that stops its loops.
- **gocryptfs-lib/main.go:** `GocryptfsOpenVFS` called `configfile.LoadBytes`,
  which exists only in a hand-patched copy of the gitignored, generated
  `internal/`. A clean checkout could not build. The config is now parsed in
  main.go (`parseConfig`).
- **Makefile:** `BIN_DIR` now comes from `swift build --show-bin-path`, because
  newer SwiftPM writes to `.build/out/Products/Debug` and `make test-logic` /
  `make app` could not find the binaries.

## 2026-10-28 — Mac app can browse vaults on an imagethat-web server

- **ImageThat/Services/RemoteServerClient.swift (new):** HTTP client for the
  webserver's JSON API: vaults, unlock/lock/logout, list, HEAD size, Range
  reads, thumbnails, upload (in-memory and streamed), mkdir, move, delete.
  *Why:* the server decrypts next to the disk, so on a slow network only the
  bytes being viewed are sent. It uses an ephemeral URLSession with no URL cache,
  so decrypted responses and the session cookie never reach disk (CLAUDE.md
  rule). The vault password stays in memory, and the client re-unlocks once if
  the server's idle timeout (30 min by default) has locked the vault.
  Query values are strictly percent-encoded, because Go reads `+` as a space.
- **ImageThat/Models/RemoteVaultSource.swift (new):** WritableMediaSource for a
  remote vault. Video goes through EncryptedResourceLoader via
  `RemoteRangeReader`. Thumbnails come from the server, with local rendering
  when the server returns 204. The server never overwrites, so saveImage uploads
  a hidden temp file, deletes the original, then renames.
- **AppState.swift:** Servers state, plus restore, add, remove, sidebar,
  unlock (session cache → saved passwords → prompt; a stale saved password
  re-prompts), mkdir/delete-folder sidebar refresh, imports, and drag-and-drop
  move/copy to and from remote vaults.
- **SidebarView / AddServerSheet (new) / ImageThatApp / VaultUnlockSheet:**
  Servers section with add/remove, a File ▸ Connect to Server… (⌘K) command,
  and the vault name shown in the unlock prompt.
- **MediaTransfer.swift:** remote items can be dragged.
- **Info.plist:** `NSAllowsLocalNetworking` so http:// LAN servers work in the
  .app bundle.
- **Tests/RemoteServerTest.swift (new):** starts the real webserver binary and
  checks unlock, browse, edits, a streamed 163 MB upload, byte-exact ranges,
  playback with seek, re-unlock after idle timeout, and lock.
  `make test-logic` now also builds the webserver.

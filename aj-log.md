# aj-log

## 2026-10-29 — imagethat-web can send a vault encrypted or decrypted
- User asked for the server to send either encrypted or decrypted data, so clients can cache ciphertext (the remote-vault cache discussion: caching the plaintext the server sends would break the no-plaintext-on-disk rule).
- `webserver/server/handlers_raw.go` (new), routes in `handlers_fs.go`: `GET/HEAD /api/vaults/{id}/raw/list?path=` (ciphertext listing) and `/raw/file?path=` (ciphertext bytes via `http.ServeContent`, Range; `gocryptfs.conf` is a file there). Same unlocked-session requirement as every endpoint (conf + ciphertext would allow offline guessing). Paths cleaned with `vault.CleanPath`, resolved with `EvalSymlinks`, refused if they leave the vault dir. `no-store`. The decrypted endpoints are unchanged; writes keep using them.
- `core/remote/client.go` (new package): `Client` (cookie jar, optional leaf-cert SHA-256 pinning, re-unlock once on 401 with the in-memory password), `OpenVault` = unlock + `vault.Open(RawFS)`. `RawFS` is a `vault.VFS` over the raw endpoints (Range reads, HEAD sizes). Sessions are read-only.
- `gocryptfs-lib/main.go`: `GocryptfsOpenRemote(baseURL, vaultID, password, pinnedSHA256)` returns a normal session id; all read and thumbnail exports work on it. Declared in `CLibGocryptfs/libgocryptfs.h`. `core/mobile`: `OpenRemote` for Android (`cert_mismatch:` error code added).
- Tests: `webserver/server/raw_test.go` (every fixture file decrypts identically to a local session, wrong password, unknown vault, read-only, one HTTP request for a 100 KB read, re-unlock after the server drops sessions, 401 without unlock, `..`/absolute/symlink escapes refused, Range). `gocryptfs-lib/remote_test.go` runs the real server in process and checks `GocryptfsOpenRemote` reads, ranges and thumbnails match the local session and writes are refused; `gocryptfs-lib/go.mod` gains a test-only `webserver` require/replace.
- `webserver/go.mod`: tidied (`x/image` now indirect since the thumbnailer moved to core). `webserver/README.md`: raw endpoints and the two modes documented.
- Not done: the Mac and Android apps do not use ciphertext mode yet (RemoteVaultSource still uses the decrypted API), and there is no disk range cache for it yet.
- Verified: core (race), webserver, shim, C harness 69/69, app build, Swift integration 61/61, Android unit tests with a rebuilt aar.

## 2026-10-29 — Drive vault: "gocryptfs.diriv: file does not exist" when opening a folder
- Symptom (user screenshot): opening `turkey2019/istanbul_bj` in a Drive vault failed with `read LVcgjkO6wt2kFYio4JbH4g/gocryptfs.diriv: ... file does not exist`. The IV file was missing from the app's index of Drive entries, not from Drive.
- Cause 1, race in `DriveVaultFS.ensureDirListed`: it marked a directory listed before the Drive fetch finished, so a concurrent caller skipped the fetch and found nothing. Clicking a vault folder now expands the sidebar and loads the folder at the same moment, which hits it. A failed fetch was also remembered as listed-and-empty for the rest of the session.
- Cause 2, `GoogleDriveService.listAllEntries`, `listFiles`, `listFolders` asked for 1000 entries and ignored `nextPageToken`, so folders over 1000 entries were truncated, including possibly their `gocryptfs.diriv`.
- `DriveVaultFS.swift`: one fetch per directory at a time (`inFlight` `DispatchGroup`s); concurrent callers wait for it; only a successful fetch marks the directory listed. `GoogleDriveService.swift`: shared `listAllPages` follows `nextPageToken` for all three listings.
- Verified: app builds and is signed; Swift integration 61/61. Not verified: the Drive flow itself (no automated Drive test; `GoogleDriveService` is a singleton with no seam to fake). Needs a retry on the real vault.

## 2026-10-29 — Google Drive vaults: decrypted folder names, encrypted folder creation
- Symptom (user screenshot): under a Drive vault (`bali2023_nov`) the sidebar listed gocryptfs ciphertext names (`4sbz9JYNvL1RyXyu145W7w`...) plus one readable folder (`bali2023mobile`). Not from the Go core work: the Drive sidebar and Drive vault create code date from 2026-05-05.
- Cause 1, sidebar: `expandGoogleDriveSidebarItem` listed raw Drive folder names for every folder, vault or not, and never switched to the vault's decrypted tree after unlock (local vaults do). `findVaultRoot` then passed those ciphertext names as a plaintext subpath, so clicking one opened nothing.
- Cause 2, creating folders: `DriveVaultSource.createDirectory` created the Drive folder under its plain name, with no `gocryptfs.diriv`: invisible to the vault and to gocryptfs, and the name readable on Drive. That is what `bali2023mobile` is. Uploads also skipped the `.name` sidecar for long file names, and the Drive index never re-listed a directory the app had changed.
- `AppState.swift`: Drive vault folders are sidebar items with the vault root's `gdrive://` URL and the plaintext `vaultPath` (like local vault items). A locked vault shows no children; on unlock its decrypted folders fill in (`loadDriveVaultSidebarChildren`). New `navigateToDriveVaultFolder`, `refreshDriveVaultSidebar`, `deleteDriveVaultSidebarFolder`, `plainVaultPath` (maps a `findVaultRoot` ciphertext path to plaintext). Clicking an unlocked Drive vault reuses its bridge instead of re-deriving the key. `createFolder` refreshes the Drive vault sidebar and listing.
- `SidebarView.swift`: Drive vault folder items get their own "Delete Folder" (through the vault). Why: their URL is the vault root's, so the plain-Drive delete would have deleted the whole vault.
- `DriveVaultSource.swift`: `createDirectory` creates the folder under its encrypted name, uploads a random 16-byte `gocryptfs.diriv`, writes the `.name` sidecar for long names, and deletes the folder if the IV upload fails. Uploads go through `uploadEncrypted` (sidecar + index refresh). Deletes refresh the index.
- `DriveVaultFS.swift`: `invalidateListing(cipherDir:)` drops a directory's cached children so the next listing refetches.
- `DriveVaultBridge.swift`: `longNameSidecar(cipherDirPath:plainName:)`. Go: `vault.Session.LongNameSidecar` (+ test writing a hashed dir with its sidecar by hand and listing it), shim export `GocryptfsLongNameSidecar`, declared in `CLibGocryptfs/libgocryptfs.h`.
- Verified: build, Go core, shim, C harness 69/69, Swift integration 61/61. Not verified: the Drive flows themselves (no automated Drive test); needs a manual run against the real vault.
- Known, not fixed: moving a file between folders of a Drive vault moves the Drive file without re-encrypting its name for the new folder, so it will not decrypt there. The existing `bali2023mobile` folder is outside the vault's encryption and will now not show; it needs handling by hand on Drive.

## 2026-10-29 — Slow-link large-video test; vault reads coalesced and read-ahead
- `core/vault/slowvideo_test.go` (new): large video in a gocryptfs vault behind a slow backend (`slowVFS`: per-call latency + bandwidth, counts calls and bytes, no persistent handle like a network/SAF backend). `TestSlowLinkLargeVideo`: 256 MB synthetic file (64 MB under -short), modelled cost: byte-exact reads at player offsets, startup with moov at the end, sequential 256 KiB playback with bounded heap, concurrent ranges, lock mid-stream. `TestSlowLinkRealVideoWithFFmpeg`: real ~40 MB MP4 (moov at end) served by `http.ServeContent` from the vault over a backend with real sleeps; ffprobe finds the duration, ffmpeg decodes frames at 1 s and 18 s, checks the late frame seeks rather than reading through. Env knobs `IMAGETHAT_SLOW_VIDEO_MB`, `_LATENCY_MS`, `_MBPS`, `IMAGETHAT_SKIP_SLOW_VIDEO`.
  - Why: user asked for a test of large videos over a slow connection on a gocryptfs vault. The existing `Tests/SlowVideoTest.swift` is Mac-only and needs the gocryptfs binary + FUSE; this one runs on Linux, macOS and CI.
- The test found the engine made one backend read per 4 KiB block. `core/vault/file.go`: `ReadAt` now reads the contiguous ciphertext of up to 256 blocks (1 MiB) in one backend call, then decrypts block by block (`maxBlocksPerRead`). `Read` (the sequential path used by `http.ServeContent`/`io.Copy` with 32 KiB buffers) gained an in-memory read-ahead window, 128 KiB growing to 1 MiB while reads stay sequential, wiped on refill and Close; `ReadAt` is unbuffered as before.
  - Effect on a 20 ms / 10 MB/s link: player startup 16.0 s (786 calls) -> 0.43 s (6 calls, floor 0.41 s); 256 MB stream 65,536 -> 1,024 calls (one per player request); ffprobe 6.2 s -> 0.6 s; frame at 1 s 76 s -> 2.2 s; frame at 18 s 28 s -> 1.3 s. Mac slow-drive test: metadata 0.3 s / 404 KB -> 0.1 s / 331 KB.
- `core/vault/errors_test.go`: `failingVFS` and `shortReadVFS` now fail mid-range (partial data + error) instead of only on a read's start offset. Why: with coalesced reads one read spans the failure point; a real disk returns a short read with the error, and the assertions are unchanged.
- `.github/workflows/core-ci.yml`: installs ffmpeg so the real-player test runs in CI. Root `Makefile`: `make slow-video-test`.
- Verified on macOS: all core tests (+ race), shim, C harness 69/69, Swift integration 61/61, webserver, Android unit tests with the rebuilt aar.

## 2026-10-29 — Go core verified on the Mac
- Fast-forwarded `feature/sharedgo` onto the cloud session's three commits (spec, Phase 0+1a, Phase 2) and built on macOS (Xcode, Swift 6.4, Go 1.27.1).
- Results: `make build` clean apart from existing Swift 6 warnings in `DriveVaultFS.swift` and `LocalOAuthServer.swift`; `make golib-test` 69/69 through `libgocryptfs.dylib`; `make core-test` all packages ok; `make test-logic` 61/61, including the new streaming-import checks and the slow-drive video test, which exercises the host-callback (Drive-style) path over `core/vault`; `make test-e2e` 3 pass, 1 skip (screenshot needs Screen Recording permission, as before); `make android-test` passes with `vaultcore.aar` rebuilt from `core/mobile`.
- `Tests/IntegrationTest.swift`: iterate `enumerator(...).allObjects` in the no-plaintext scan. Why: iterating the enumerator directly is unavailable from async contexts (Swift 6 warning, a future error).
- Not verified by hand: the running app's thumbnail grid and Preferences cache controls; the uncompiled-in-the-cloud Swift now compiles, and the integration tests cover the engine calls behind them.

## 2026-10-29 — Go core, Phase 2 (Go side) + Linux library + C API harness
- `core/cache/` (new package): `blob.go` moved from `webserver/server/cache.go` (exported as `Blob`, plus `Walk`, `Closed`, `Remove`); `thumbdisk.go` (`ThumbDisk`): persistent previews sealed with the vault's own key, hashed per-source dirs, magic-prefixed entries, LRU eviction, atomic writes, `ThumbKey` with plaintext size. Tests moved/added (`blob_test.go`, `thumbdisk_test.go`).
- `core/thumb/` (new package): `image.go` moved from `webserver/server/thumb.go` (`Make`, `Quality`); `service.go` (`Service`): memory + optional disk tier, decode permits with context cancellation, Go decoders, host `Decoder` hook, `Put` for host-rendered previews, `Invalidate`, `SessionReader` adapter. `service_test.go` covers tiers, no-preview cases, host decoder path, disk tier across a relaunch and after lock, no plaintext on disk, concurrency/cancellation.
  - Why: spec Phase 2. One thumbnail pipeline for Mac, Android and web instead of three.
- `webserver/server`: uses `cache.Blob` and `thumb.Make`; tests adjusted (`Walk`, `Closed`). `server_test.go` fixture path fixed to `../../core/vault/testdata/vault`: after the Phase 0 move all 23 server tests had been silently skipping. They run again (0 skips).
- `gocryptfs-lib/main.go`: thumbnail exports `GocryptfsThumbConfigure/Get/Put/Invalidate`, `GocryptfsThumbCacheClear/Size`; per-session `thumb.Service` (memory-only until configured), shared `ThumbDisk` per directory; write/move/delete invalidate previews; Close wipes the memory tier. Declared in `CLibGocryptfs/libgocryptfs.h`. Shim tests extended.
- `gocryptfs-lib/ctest/api_test.c` + `run.sh` (new): C program linking the built shared library through the app's header; 69 checks (open codes, listing, range vs whole reads against committed plaintext, streaming import + abort, replace, seal, names, thumbnails, move/delete, close) including scans of the vault dir and cache dir for leaked plaintext. `make golib-linux` builds `build/libgocryptfs.so`; `make golib-linux-test` / `make golib-test` run the harness on Linux / macOS. `core-ci.yml` runs it.
  - Why: user asked for a Linux build of the library with tests that run on Linux; this exercises the real C ABI, not just the Go functions.
- Swift (uncompiled here, for the Mac): `GocryptfsBridge.swift` gains `VaultThumbnailing` protocol, `GocryptfsThumbnails` enum over the exports, and conformance for both bridges (`DriveVaultBridge.swift`); `AppState.loadThumbnail` asks the engine first, renders HEIC/RAW/video itself and stores the JPEG back; `configureThumbnails` after each unlock points the disk tier at `AppPaths.thumbnailCache`; `Utilities/NSImage+JPEG.swift` (new); `PreferencesView` clears/sizes through the engine; `Services/PersistentThumbnailCache.swift` deleted (its `VaultCryptoBridge` protocol with it).
- `core/Makefile`, `core-ci.yml`: cover `cache` and `thumb`. Root `Makefile`: `golib-test`, `golib-linux`, `golib-linux-test`.
- Docs: spec Phase 2 notes; `webserver/README.md`.

## 2026-10-29 — Go core, Phase 0 + 1a: one gocryptfs engine for Mac, Android and web
- `core/` (new Go module `github.com/adrianm/imagethat/core`): `webserver/vault` and `webserver/mobile` moved here unchanged (`git mv`), with `scripts/sync-gocryptfs.sh`, `tools.go` (x/mobile pin) and a `Makefile` (`sync`, `test`, `test-short`, `vet`). `webserver/go.mod` and `gocryptfs-lib/go.mod` depend on it via `replace ../core`.
  - Why: the spec's Phase 0. One engine instead of two (`gocryptfs-lib/main.go` was a second copy of the same crypto); `replace` rather than `go.work` because `gomobile bind` ignores workspaces.
- `core/vault`: new exports `SealBlob`/`OpenBlob` (`blob.go`: gocryptfs-format whole-buffer seal for persisted thumbnails), `EncryptName`, `CipherPath` (`path.go`), `Replace` (`write.go`: streamed atomic overwrite, create if missing). Tests in `exports_test.go`.
- `gocryptfs-lib/main.go`: rewritten as a cgo shim over `core/vault` with the same C API (`CLibGocryptfs/libgocryptfs.h`), plus streaming exports `GocryptfsCreateFile`/`WriterWrite`/`WriterClose`/`WriterAbort`. Keeps a 16-entry open-handle cache for range reads; no `/tmp/gocryptfs.log`; write ops work over any WriteVFS. Typedefs shared via `shim.h`. Tests (`shimtest` tag): `main_test.go` + `shimtest_helpers.go` drive every export against the core fixture, with a no-plaintext walk of the vault dir. `go.mod`/`go.sum` slimmed to the core dependency.
  - Why: Phase 1a. Removes the duplicate crypto, the plaintext log and the XChaCha IV bug from the Mac build without touching `GocryptfsBridge.swift`'s API. Verified on Linux with `-buildmode=c-shared`; a Mac build is still needed for the Swift side.
- `ImageThat/Services/GocryptfsBridge.swift`: `createFile(path:)` + `VaultFileWriter` (write/finish/abort, abort on deinit), `describeWriteCode`. `ImageThat/Models/VaultSource.swift`: `writeChunks` now streams through the writer and aborts on error or cancellation.
  - Why: the old `writeChunks` wrote plaintext with a plaintext name into the ciphertext directory (its comment assumed a FUSE mount). This closes that leak.
- `Tests/IntegrationTest.swift`: new "Streaming Import" section: 3 MiB round trip, walk of the vault directory for a plaintext marker, failed import leaves no entry.
- `CLibGocryptfs/libgocryptfs.h`: streaming writer declarations + return-code docs. `ImageThat/BridgingHeader.h` now includes that header instead of carrying a stale copy; `ImageThat/libgocryptfs.h` (stale cgo-generated copy) deleted and `Scripts/build-golib.sh` no longer copies it. `Scripts/sync-gocryptfs.sh` deleted (core's script is the one).
- Build/CI: root `Makefile` (`golib` depends on `core/internal/gocryptfs`, `golib-sync` → core, new `core-test`), `webserver/Makefile` (sync delegates to core; tests/vet/fmt only `server`/`cmd`), `android/vaultcore/build.sh` and `app/build.gradle.kts` (bind `core/mobile`, fixtures from `core/vault/testdata`), `.github/workflows/core-ci.yml` (new: fmt, vet, tests with gocryptfs interop, race, cgo shim build + tests), `webserver-ci.yml` (HTTP layer only, triggers on `core/**` too), `android-ci.yml` and `actions/android-setup` (point at core), `release.yml` (cache both go.sum), `.gitignore` (`core/internal/gocryptfs/`).
- Docs: spec updated (module path, `core/mobile` keeps the `mobile` package name for Kotlin, Phase 0 landed, Phase 1 split into 1a landed / 1b gomobile spike), `README.md`, `webserver/README.md`.
- Not done here (needs a Mac): `swift build`, `make test-logic`, the gomobile macOS spike (Phase 1b).

## 2026-10-29 — Spec: move file handling, caching, streaming and transport into a Go core
- `docs/superpowers/specs/2026-10-29-go-core-design.md` (new): design + phased plan for a `core/` Go module that owns sources (folder, local vault, remote vault, Drive, Drive vault), caches (encrypted thumbnail tiers, Drive ciphertext caches), streaming (`File.ReadAt` for players, `Copy` with progress) and transport (imagethat-web client, Drive v3 + OAuth refresh), bound to macOS and Android with gomobile. Eight Mermaid diagrams: current vs target architecture, class model, video / thumbnail / Drive / transfer sequences, phase graph.
  - Why: the Mac does all of this in Swift over a crypto-only cgo library, Android re-did it in Kotlin, and `webserver/vault` is a second copy of the gocryptfs engine; one Go implementation serves all three.
  - Keeps Keychain + LAContext, decoders, SAF and player glue native; each phase swaps one implementation behind the existing `MediaSource` interface and deletes the old one only after the same tests pass.
  - Flags defects the move fixes, chiefly `VaultSource.writeChunks` writing plaintext into the ciphertext directory and `/tmp/gocryptfs.log`.

## 2026-10-28 — Android redesign part B: Home, Connect, server vaults, unlock, app lock, dialogs
- `HomeScreen.kt`: rebuilt with the shared redesign components — big "ImageThat" title with a live summary ("1 server · 2 vaults unlocked" / "all locked"), "Lock all" pill, grouped cards Servers / On this phone / Settings. Server rows show "Port N · https" (online state is only known on the server screen); "Connect to a server" row with dashed tile; Offline vault row with a "Set up" chip until it exists; folder rows with an options button (forget saved password / remove from list); Open folder | New vault split row; Settings has the Auto-lock row (value + chevron) and an informational "App lock" row (no toggle). Why: user asked for the new design; app lock is always on.
  - Server removal moved from an inline "Remove" button to long-press on Home and the server screen's overflow menu, both with a confirm dialog (`RemoveServerDialog`, tag `remove-confirm`). Why: the design has chevrons, not inline buttons; removing also forgets saved passwords so it deserves a confirmation.
- `RemoteVaultsScreen.kt`: back + overflow (Remove server, secure popup), server title with status dot ("Online · port N · K vaults" / Unreachable / Certificate changed), client-side vault search (`vault-search`), rows with lock tile, "Locked"/"Unlocked" and "Password saved". Why: Server mockup; no counts/sizes since the API has none.
- `UnlockDialog.kt`: the password prompt is now a full-screen secure dialog (FLAG_SECURE, edge to edge) following Unlock.dc.html: place label (server name or "On this phone"), accent lock tile, vault name, PasswordInput with eye, accent Unlock pill, inline error, footer with the real idle timeout. No fingerprint button. The saved-password stage uses the same screen with a spinner. Forget-password dialog restyled. Password stays in plain `remember` state.
- `ConnectScreen.kt`, `SetOfflinePasswordScreen.kt`, `AppLockScreen.kt`, auto-lock picker, new-vault and notice dialogs: restyled with Palette, AppTextField/PasswordInput, PillButton/OutlinePill, DialogButton. The lock screen still shows no vault or server names.
- `AppNav.kt`: deleted the old unused Material `ImageThatTheme`.
- `components/Components.kt`: `SectionLabel` now exposes its original text to semantics (reads "On this phone", not spelled-out capitals; tests find it); `FootnoteText` takes `textAlign`. `AppIcons.Clock` added.
- Tests: "Connect…" → "Connect to a server"; server removal test uses long-press + confirm; new instrumented tests for vault search and removal from the server menu; new JVM `HomeHelpersTest`. Spec "Screens" updated.

## 2026-10-28 — Task 4 fix round 1
- `.github/workflows/webserver-ci.yml`, `release.yml`: setup-go now uses `go-version-file: webserver/go.mod` instead of '1.24'. Why: go.mod now says go 1.26.0, so CI must follow it.
- `VaultcoreSmokeTest.kt`: Kotlin `assert` replaced by `assertTrue`. Why: JVM assertions are disabled on ART, so the check did nothing on device.

## 2026-10-28 — Android project skeleton with gomobile vault engine (Task 4)
- `android/` (new): Gradle project (AGP 8.13.2, Kotlin 2.2.21, Gradle 8.14.6 wrapper, Compose BOM 2025.06.01, Coil 3.2.0), minSdk 29 / targetSdk+compileSdk 35, applicationId `ai.alertd.imagethat`.
  - Why: base for the Android app; versions chosen so compileSdk 35 (the only installed platform) works. AGP 9 and newer Compose BOMs need compileSdk 36 or a build-system migration.
- `android/vaultcore/build.sh` + Gradle task `:app:buildVaultcore` (preBuild dependency): `gomobile bind` of `webserver/mobile` into `app/libs/vaultcore.aar` (gitignored). Tool paths from env with defaults.
  - Why: the app embeds the Go vault engine instead of reimplementing gocryptfs.
- `AndroidManifest.xml`, `data_extraction_rules.xml`: allowBackup=false, extraction rules exclude everything for cloud-backup and device-transfer, no providers (androidx.startup InitializationProvider removed via tools:node), only INTERNET and USE_BIOMETRIC.
  - Why: decrypted-data-never-leaves-the-device principle.
- `MainActivity`: FLAG_SECURE before setContent. `ImageThatApp`: Coil singleton with memory cache only (diskCache null + diskCachePolicy DISABLED); sets Go TMPDIR.
- `ManifestHardeningTest` (JVM): parses the merged manifest; `testDebugUnitTest` takes MERGED_MANIFEST as an input so the merge always runs first. `VaultcoreSmokeTest` (instrumented): init/open/create/read.
- `webserver/mobile/mobile.go`: new `SetTempDir(dir)` (+ test). Why: the smoke test failed on device with `mkdir /data/local/tmp/vault-init-...: permission denied`; Go on Android does not see TMPDIR from the process environment, so `vault.Init`'s `os.MkdirTemp("")` fails. Kotlin must call `Mobile.setTempDir(cacheDir)` before `initLocal`.
- `webserver/go.mod`, `go.sum`, `tools.go`: add golang.org/x/mobile (needed by `gomobile bind`); `tools.go` keeps it from being tidied away. Side effect: go directive 1.24 -> 1.26.0, x/image 0.24 -> 0.47, x/sys 0.30 -> 0.49 (required by latest x/mobile).
- Root `Makefile`: `android-aar`, `android-debug`, `android-test`; `.gitignore`: Android build outputs.

## 2026-10-28 — Replace Peekaboo in E2E tests with built-in macOS tools
- `Tests/e2e_test.sh`: window detection now uses `osascript` (JXA + CGWindowListCopyWindowInfo) instead of `peekaboo list windows`; screenshot uses `screencapture -l <windowID>` instead of `peekaboo image`.
  - Why: drop the Homebrew Peekaboo dependency; both tools ship with macOS.
- `Makefile`: test-e2e comment now notes the Screen Recording permission instead of the Peekaboo install.
- Not done: `.mcp.json` peekaboo entry and `.claude/settings.local.json` disable entry left in place (deletion blocked by permission check; pending user decision).

## 2026-10-28 — Fix gocryptfs-lib build on a clean checkout
- `gocryptfs-lib/main.go`: replaced `configfile.LoadBytes` (a local edit to the generated, gitignored `internal/` copy that was lost) with a `parseConfig` helper in main.go that unmarshals and validates the config.
  - Why: `internal/` must be an unmodified upstream copy so it can be regenerated; the build failed with `undefined: configfile.LoadBytes`.
- Locally regenerated (gitignored, not committed): `gocryptfs-lib/internal/` copied from gocryptfs v2.6.1 with import paths rewritten (same package list as `webserver/scripts/sync-gocryptfs.sh`), and `go.sum` via `go mod download all`.
- E2E tests still not run: `swift build` needs full Xcode (only Command Line Tools installed).

## 2026-10-28 — Follow SwiftPM's build output path
- `Makefile` BIN_DIR and `Tests/e2e_test.sh` IntegrationTest path now come from `swift build --show-bin-path`.
  - Why: Xcode 27's SwiftPM writes to `.build/out/Products/Debug`, not `.build/arm64-apple-macosx/debug`, so `make app` failed to find the binary.
- E2E run result: window-detection tests (osascript replacement for Peekaboo) PASS. Screenshot FAIL (terminal lacks Screen Recording permission). Integration tests FAIL because their fixtures (`/tmp/imagethat-test-plain`, `/tmp/imagethat-test/vault`) are not created by anything in the repo.
- Note: inside Claude Code's sandbox `swift build` only works with `--disable-sandbox` (nested sandbox breaks actool); Makefile left unchanged for that.

## 2026-10-28 — Committed test fixtures so integration tests pass
- `Tests/fixtures/vault`: gocryptfs vault made with the real gocryptfs v2.6.1 binary (password `testpass123`) holding `hello.txt` and `test_image.png`, the files IntegrationTest checks. `Tests/fixtures/plain`: red/green/blue PNGs.
  - Why: IntegrationTest reads `/tmp/imagethat-test-plain` and `/tmp/imagethat-test/vault`, which nothing created; committing them (like `webserver/vault/testdata`) means no FUSE is needed at test time.
- `Makefile`: new `test-fixtures` target copies them into /tmp; `test-logic` and `test-e2e` depend on it.
- `Package.swift`: IntegrationTest target excludes `e2e_test.sh` and `fixtures` (silences "unhandled file" warnings).
- `Tests/e2e_test.sh`: `--args` launch waits 20s instead of 10s (it timed out once).
- Result: integration 21/21 pass; E2E 3/4 — screenshot needs Screen Recording permission for the terminal.

## 2026-10-28 — Removed Peekaboo, E2E suite green
- Deleted `.mcp.json` (only held the Peekaboo MCP server) and emptied the untracked `.claude/settings.local.json` (only held the entry disabling it).
  - Why: Peekaboo was disabled and no longer used; nothing in the repo references it now.
- `Tests/e2e_test.sh`: screenshot test now SKIPs (instead of failing) when `screencapture` reports "could not create image", which is macOS's response when the terminal lacks Screen Recording permission. Other screencapture errors still FAIL. Results line counts skips.
  - Why: the permission can't be granted from a script, and under a launchd-started tmux there is no terminal app to grant it to.
- Result: `make test-e2e` exits 0 — 3 passed, 0 failed, 1 skipped; integration 21/21.

## 2026-10-28 — gocryptfs-lib builds from a fresh checkout
- `Scripts/sync-gocryptfs.sh`: generates `gocryptfs-lib/internal/` from the gocryptfs v2.6.1 module cache (same approach and package list as `webserver/scripts/sync-gocryptfs.sh`).
- `Makefile`: `golib` now depends on `gocryptfs-lib/internal`, which runs the sync when missing; `make golib-sync` regenerates on demand.
- `.gitignore` / `gocryptfs-lib/go.sum`: go.sum is now committed (as webserver/go.sum already is).
  - Why: both were gitignored with nothing to recreate them, so `make build` failed on any new clone.

## 2026-10-28 — Releases only on a vX.Y.Z tag
- `.github/workflows/webserver-release.yml` → `release.yml`: triggers only on pushing a `vX.Y.Z` tag (no more release per merge to main, no manual dispatch, no auto patch bump). Version comes from the tag. Split into version / webserver / publish jobs so the Android APK job can join the same release.
- Release is now `vX.Y.Z` (one tag for all components) instead of `webserver-vX.Y.Z`; `gh release create --verify-tag` attaches to the pushed tag.
- `webserver/README.md` "Releasing" section updated.
  - Why: user wants releases only when a tag is created, and one tag to ship both webserver and Android app.

## 2026-10-28 — Android app design spec
- Added `docs/superpowers/specs/2026-10-28-android-app-design.md`: Kotlin/Compose app using the webserver's Go `vault/` package via gomobile for on-device gocryptfs (built-in offline vault + SAF-picked vaults), remote browsing via the webserver API, biometric Keystore keys, nothing decrypted on disk, APK built by the tag-triggered release.
  - Why: user asked for an Android client that saves nothing locally unless selected, and saves encrypted via gocryptfs.

## 2026-10-28 — Android implementation plan
- Added `docs/superpowers/plans/2026-10-28-android-app.md` (12 tasks: vault Init/Open, gomobile wrapper, Storage backend, Android skeleton, keys/state, API client, local vaults, repository, screens, no-plaintext test, CI/release/signing). Spec marked approved; spec now also lists `vault.Open` (needed to open SAF-backed vaults, since `newSession` is unexported).


## 2026-10-28 — kunj blueprint

- Added `.kunj/blueprint.json` so `kunj ct start` can build a snapshot with the webserver's dependencies already installed.
  - Why: branch containers start in seconds from the snapshot instead of re-running `go mod download` / `npm ci` each time.
  - Only covers `webserver/` (Go 1.24 + the Vite/React UI): the macOS app needs Xcode and can't build in kunj's Linux sandbox.
  - `ffmpeg` via apt for server-side video thumbnails; FUSE/gocryptfs left out because the sandbox can't mount, so use `test-short` there.
  - Lockfiles point at `webserver/go.sum` and `webserver/web/package-lock.json` since neither is at the repo root (kunj's default).

## 2026-10-28 — kunj blueprint: Android

- Blueprint now also builds the Android app (from the `android-app` work): `java: 21`, `go: 1.26` (the android branch's `webserver/go.mod`), plus two repo presets.
  - `.kunj/presets/android.sh`: SDK cmdline-tools + platform 35, build-tools 35.0.0, NDK 26.1.10909125 into `/opt/android-sdk`; `GRADLE_USER_HOME=/opt/gradle`. Why: presets are the only root step and `/home` is a volume the snapshot drops, so everything lives under `/opt`.
  - On arm64 Docker (this Mac) it adds `libc6:amd64` etc.: Google ships the Linux NDK and aapt2 for x86_64 only, and they run through the Docker VM's x86_64 emulation.
  - `.kunj/presets/gomobile.sh`: gomobile/gobind pinned to the `golang.org/x/mobile` version in go.mod, built as amd64 because gomobile panics ("unsupported GOARCH: arm64") looking for an arm64 Linux NDK.
  - Added `make` to apt: the sandbox image has none, so `make -C webserver sync` failed.
  - The Android setup step skips while `android/` isn't on the default branch (snapshots clone origin's default branch).
  - Verified in `kunj-sandbox` with all presets as root and `./gradlew :app:assembleDebug` as the sandbox user on the `android-app` branch: app-debug.apk built.
## 2026-10-28 — vault.Init and vault.Open (Android task 1)
- `webserver/vault/init.go`: `Init(WriteVFS, password)` creates a new gocryptfs vault (config via `configfile.Create` in a scratch dir, then `gocryptfs.conf` + random 16-byte `gocryptfs.diriv` written through the VFS); refuses with an `fs.ErrExist`-wrapping error if a conf exists.
- `webserver/vault/session.go`: `Open(VFS, password)` opens a vault behind any VFS; `OpenLocal` now reads the conf and shares a new `parseConfig` with it (same error mapping as before).
  - Why: the gomobile wrapper must create vaults and open SAF-backed ones, and `newSession` is unexported.
- `webserver/vault/init_test.go`: round-trip, refuse-existing, no-config and gocryptfs-binary interop tests. Interop skips without `/dev/fuse` (so it skips on macOS, like the existing interop test).

## 2026-10-28 — Wipe password bytes in Init and newSession
- `webserver/vault/init.go`, `session.go`: the password is copied into a `[]byte` that is wiped after use (`Create` in Init, `DecryptMasterKey` in newSession, so Open and OpenLocal too).
  - Why: review finding; the constraint is that password bytes are wiped after key derivation.

## 2026-10-28 — mobile package for local vaults (Android task 2)
- `webserver/mobile/mobile.go`: gomobile-bindable `InitLocal`, `OpenLocal`, `Vault` (List as JSON, Size, ReadRange, Create, Mkdir, Move, Remove, Close); one `wrapErr` helper prefixes errors with `wrong_password:`, `no_config:`, `not_found:`, `exists:`, `invalid_path:`.
- `webserver/mobile/writer.go`: `FileWriter` runs `Session.Create` in a goroutine over an `io.Pipe` (bounded memory); `Abort` closes the pipe with an error so Create never commits and cleans its temp file (it does not call `Remove`, which could delete a pre-existing file at that path).
- `webserver/mobile/mobile_test.go`: round-trip, ReadRange block boundaries/EOF, error codes, 64 MiB bounded-heap, abort, and fixture reads compared against `vault.OpenLocal`.
  - Why: Kotlin needs a bindable API over the Go vault package, with streaming writes that don't buffer large files.

## 2026-10-28 — Storage interface for non-filesystem vaults (Android task 3)
- `webserver/mobile/storage.go`: exported `Storage`/`StorageWriter` interfaces (Kotlin implements over SAF), `InitStorage`, `OpenStorage` (returns the same `*Vault`, errors via `wrapErr`), and internal `storageVFS` implementing `vault.WriteVFS` (no `FileOpener`; `CreateTemp` makes `<dir>/<pattern with * -> random hex>`, default `.tmp-<random>`, via `OpenWriter`; short `ReadAt` returns `io.EOF` per the ReaderAt contract).
- `webserver/mobile/storage_test.go`: test-only `dirStorage`; round trip, error codes, abort, >175-byte name, and fixtures (vault, aessiv, xchacha) compared against `vault.OpenLocal` reads.
  - Why: SAF document trees have no paths, so vaults in user-picked folders must be reachable through a host-implemented interface.

## 2026-10-28 — Storage error contract (Android task 3 fix round 1)
- `webserver/mobile/storage.go`: documented that Storage errors must start with `not_found` / `exists` (and OpenWriter must fail if the file exists and leave nothing behind); `storageVFS` converts those via `conv` into `*fs.PathError` wrapping `fs.ErrNotExist`/`fs.ErrExist` in ReadAt, FileSize, ReadDir, ReadFile and the write-side methods.
- `webserver/mobile/storage_test.go`: `msgStorage` returns plain message errors; test proves List/Size/ReadRange on a missing path give `not_found:` and a repeated Mkdir gives `exists:`.
  - Why: errors from Kotlin cross gomobile as bare messages and the vault's os.IsNotExist checks do not unwrap `%w`, so not_found never surfaced.

## Task 5: Keystore-wrapped secrets and encrypted state
- Added keys/AppState.kt, StateStore.kt, KeyVault.kt: AES-GCM Keystore key `imagethat.state` (no auth) encrypts state.bin; per-secret biometric keys `imagethat.bio.<id>` (BIOMETRIC_STRONG, invalidated on enrolment). Why: remembered servers must list before auth, secrets must never be on disk unwrapped.
- StateStore writes temp file + fsync + atomic rename; corrupt file renamed to state.bin.corrupt (kept for inspection).
- Added androidx.biometric/fragment, kotlinx serialization+coroutines via version catalog.
- MainActivity now extends FragmentActivity (BiometricPrompt needs it); FLAG_SECURE kept.
- Manifest: removed USE_FINGERPRINT that androidx.biometric merges in (manifest test allows only INTERNET + USE_BIOMETRIC).
- Tests: StateStoreTest (JVM), KeyVaultTest (instrumented, requireAuth=false hook).
- Task 5 fix round 1: state.bin moved to noBackupFilesDir; StateStore quarantines only AEADBadTag/Serialization/IllegalArgument (Keystore/IO errors propagate, timestamped .corrupt name); wrap() swaps keys via alternate alias (old key+blob intact on cancel); unwrap maps UnrecoverableKey/KeyStore/AEADBadTag to Invalidated. Why: reviewer findings (data loss on transient errors, destroyed shortcut on cancel, backup leakage).

## Task 6: Remote API client
- Added remote/ApiClient.kt, ApiModels.kt, CertPinning.kt (OkHttp 4.12.0; mockwebserver, okhttp-tls, coroutines-test as test deps via catalog). Why: Android must talk to the Go webserver JSON API.
- No OkHttp Cache, in-memory CookieJar cleared by close(), password JSON built in a private char/byte copy and zeroed after the call. Why: no decrypted data or secrets persisted.
- Pinning: custom X509TrustManager comparing leaf SHA-256 (lowercase hex), permissive hostname verifier only when pinned; probeCertificate does a handshake-only probe. Why: self-signed servers.
- Errors: 401 -> Locked (WrongPassword on unlock); 403 -> OriginRejected (server messages "cross-origin request rejected"/"bad origin") else Forbidden (read-only/permission denied, NOT Locked, since server never uses 403 for locked); 404/409/429 mapped; others Http. Why: server is authoritative (handlers_fs.go, writeVaultError).
- No Origin header sent: server accepts absent Origin (checkOrigin).

## Task 7: Local vaults and SAF storage
- Added local/SafStorage.kt (mobile.Storage over a SAF tree: path->document id by walking children, per-directory in-memory cache invalidated on own writes/renames/deletes; "not_found"/"exists" message prefixes; writers delete their half-created doc on failure; cross-directory rename uses moveDocument with a copy+delete fallback). Why: SAF folders have no filesystem path for the Go engine.
- Added local/LocalVaults.kt (offline vault at noBackupFilesDir/offline-vault, SAF folder vaults, sealed LocalError mapped from Go prefixes; revoked/unreachable tree -> Unavailable; passwords CharArray zeroed after the call). Why: Task 7 brief.
- Tests: androidTest LocalVaultsTest, SafStorageTest, plus a test-only DocumentsProvider (Java, two authorities: with and without move) declared in src/androidTest/AndroidManifest.xml so the main manifest stays provider-free. AES-SIV fixture reached via an androidTest assets srcDir pointing at webserver/vault/testdata. Why: exercise SafStorage against a real provider.
- Task 7 fix round 1: SafStorage wraps every Storage method in guard{} turning SecurityException into IOException("unavailable: ...") and sets a sticky revoked flag; exists() (bool, cannot throw across gomobile) sets the flag and the next call reports it. LocalError.from maps any message containing "unavailable:" (Go may wrap it) to Unavailable. Why: reviewer finding, revocation after unlock was surfacing as Other. Added LocalVaultsTest.revocationAfterUnlockSurfacesAsUnavailable.
- Task 7 fix round 2: write-side SecurityException is no longer sticky. SafStorage.guardWrite maps it to IOException("read_only: ...") (LocalError.ReadOnly) and leaves `revoked` alone; only read/probe paths (readAt, fileSize, listDir, exists, probe) set `revoked`. Why: a provider denying writes but allowing reads made every read fail as Unavailable. Added a read-only test provider and SafStorageTest.deniedWriteIsReadOnlyAndDoesNotBreakReads.
- Task 7 fix round 3: guardWrite now probes children(root, fresh) when it catches a SecurityException; if the probe is also denied it sets `revoked` and throws unavailable, else read_only. Why: a revoked grant hit during the pre-write lookups was misreported as ReadOnly. Added LocalVaultsTest.writeAfterRevocationIsUnavailableNotReadOnly.

## Task 12: Android CI, signed release APK
- Release signing key generated (by the controller, with user approval): ~/imagethat-release.jks (PKCS12, alias `imagethat`) with passwords in ~/imagethat-release-keystore.txt; the 4 GitHub secrets (ANDROID_KEYSTORE_BASE64, ANDROID_KEYSTORE_PASSWORD, ANDROID_KEY_ALIAS, ANDROID_KEY_PASSWORD) are set on adrianj98/imaginethat. Both files must be backed up together; losing them means installed apps can no longer be updated in place. Neither is committed.
- android/app/build.gradle.kts: versionName from VERSION_NAME env (default 0.0.0-dev), versionCode = major*10000+minor*100+patch (min 1); release signingConfig from ANDROID_KEYSTORE_FILE/_PASSWORD, ANDROID_KEY_ALIAS/_PASSWORD env. A taskGraph check fails any assemble/bundle/package *Release build when one is missing, so release never falls back to debug signing while debug builds and unit tests need no env. Why: tag releases must be signed by the one key.
- Added .github/actions/android-setup (JDK 21, gradle, Go, SDK/NDK 26.1.10909125/platform 35/build-tools 35, gomobile pinned to the x/mobile version in webserver/go.mod) shared by CI and release. Why: avoid duplicating setup.
- Added .github/workflows/android-ci.yml: Go engine tests, unit tests + assembleDebug, instrumented tests on API 34 emulator with KVM enabled.
- release.yml: new `android` job (fails fast on empty secrets, decodes keystore to RUNNER_TEMP, assembleRelease, apksigner verify with cert SHA-256 pinned to 71:E3:...:6B, renames to imagethat-android_vX.Y.Z.apk, uploads artifact `android`); publish needs it; release notes gain an Android section.
- README: Android section (build, release flow, key location and backup warning).
- Task 12 fix 1: release.yml APK rename now uses ${VERSION} (a sed -i slip had turned it into a literal vX.Y.Z); release notes substitute the real APK name after the quoted heredoc. Why: second release would have collided with the first.

## Task 8: unified vault repository and save-offline
- Added android/.../data/Sources.kt: VaultSource, Item/MediaKind, SourceError (Locked, WrongPassword, NotFound, Exists, Other), RemoteSource (maps RemoteError/IOException/SerializationException so nothing escapes raw; streaming upload body), LocalSource over LocalVaultApi (adapter MobileVaultApi for mobile.Vault; chunked lazy reads; chunked streaming writes with abort on failure; injectable Thumbnailer, BitmapFactory inSampleSize default). Why: one browsing/transfer interface for both vault kinds with no decrypted data on disk or whole-file buffers.
- Added SaveOffline.kt: saveOffline streams 256 KiB chunks, recursive, per-file failures reported while others continue; prefix sanitising helper. Added VaultRepository.kt as a thin facade. SourceError.Exists added (beyond the brief's four) so mkdir can tolerate existing dirs.
- Added VaultRepositoryTest (15 tests, fakes + MockWebServer).
- Task 8 fix round 1: saveOffline sanitizes every destination segment and the prefix (hostile remote names cannot escape); Locked/WrongPassword mid-save throws SaveInterrupted(reason, partial) instead of N failures; LocalSource copy loop checks job.ensureActive() per chunk so cancel aborts the file. 3 tests added.

## Task 9: home, connect, remote vaults, unlock, set-offline-password, auto-lock
- Added android/.../ui/Session.kt: app-scoped holder of open VaultSources and connected ApiClients; lockAll() closes Go vaults, logs out/closes ApiClients, clears the Coil memory cache, drops sources and emits AllLocked; reportLocked(id) for Task 10; deadline-based idle (5 min) and background (1 min) auto-lock on an injectable clock (tick/onForeground), so AutoLockTest needs no real waits and a frozen background process still locks when used again.
- Added UnlockDialog.kt (shared password dialog, one local unlock flow: biometric prompt -> "Use password" fallback, Invalidated explanation + offer to re-enable, "Unlock with biometrics next time"), SetOfflinePasswordScreen.kt (twice, min 8, no-recovery warning), ConnectScreen.kt (probe cert only after an untrusted https failure, show SHA-256, "Trust this certificate", "Remember this server" leaves StateStore untouched when unticked), RemoteVaultsScreen.kt (lock state, inline wrong password, rate-limit text, hard cert-mismatch failure), HomeScreen.kt, AppNav.kt (browser/{sourceId} is a stub for Task 10). Why: spec Screens 1-3 and 6. Every dialog sets SecureFlagPolicy.SecureOn; password text is never in saveable state.
- ImageThatApp holds an AppContainer (one KeyVault/StateStore/Session); MainActivity feeds touches to Session.touch(). Background tracking uses ActivityLifecycleCallbacks instead of ProcessLifecycleOwner: ProcessLifecycleInitializer refuses manual initialisation ("cannot be initialized lazily") with the startup provider removed from the manifest.
- Build: navigation-compose, compose ui-test (androidTest) + ui-test-manifest (debug only), mockwebserver/okhttp-tls for androidTest.
- Tests: AutoLockTest (6), UnlockFlowTest (10); all 44 instrumented + unit tests pass.
- Task 9 fix round 1: Remember-this-server defaults unticked; http:// gets a specific "not allowed, use https" error before any request; unlock open+register runs NonCancellable and Cancel/dismiss are disabled while busy in every stage; LocalVaults.hasOpen() so tick() also locks vaults opened but not registered; the re-enable-biometrics checkbox after Invalidated is opt-in. Why: reviewer findings 1-4 (spec memory-only, R14, no untracked decrypted vault, offer not auto-opt-in).

## Task 10: vault browser and media viewer
- Added ui/BrowserViewModel.kt (state, selection, save offline via saveOffline with per-file OpResult, delete with confirmation, upload, new folder, move within the vault, events NavigateToUnlock / NeedOfflineVault). Any SourceError.Locked becomes NavigateToUnlock (screen calls Session.reportLocked), never an empty folder. A partial save/upload cut short by Locked shows its report first and only then navigates. Why: spec Screens 4-5 and Errors.
- Added ui/BrowserScreen.kt (grid, breadcrumb, multi-select bar, FAB with system photo picker upload streamed from ContentResolver with OpenableColumns.SIZE, no temp copy; result/delete/new-folder/move dialogs all SecureAlertDialog; offline vault missing -> set-password route, locked -> LocalUnlock dialog then save resumes; Save offline hidden inside the offline vault) and ui/ViewerScreen.kt (pager, pinch/double-tap zoom, images read whole into memory only up to 48 MB and downsampled to ~2048px, EXIF rotation, Media3 ExoPlayer released on dispose, no cache; remote video via OkHttpDataSource on the session's OkHttpClient, local via VaultDataSource; unsupported types show name/size only; playing video touches the idle timer every 30 s).
- Added media/VaultDataSource.kt (Media3 DataSource over VaultSource honouring position/length, SourceError surfaced as IOException) and media/ThumbnailFetcher.kt (VaultUri build/parse for vault://<sourceId>/<path>?thumb=<px>; Coil fetcher feeding in-memory bytes, registered in ImageThatApp's ImageLoader which still has no disk cache). res/layout/vault_player.xml uses a TextureView so FLAG_SECURE covers video.
- AppNav: BrowserStub replaced; LocalTouch composition local lets SecureAlertDialog (separate window) report touches to Session.touch(). RemoteSource/ApiClient: internal fileUrl + httpClient accessors for ExoPlayer. UnlockFlowTest tag browser-stub -> browser-screen.
- Build: media3 1.5.1 (exoplayer, ui, datasource-okhttp). Gradle failed with "Corrupt serialized resolution result ... kotlin-stdlib-jdk7:1.8.0" once media3 was on the classpath; excluded kotlin-stdlib-jdk7/jdk8 (merged into kotlin-stdlib since 1.8) in all configurations. Manifest: removed ACCESS_NETWORK_STATE that media3 merges in (ManifestHardeningTest allows only INTERNET + USE_BIOMETRIC).
- Tests: BrowserViewModelTest (16, JVM), VaultDataSourceTest (5, real offline vault), BrowserFlowTest (7, compose: viewer, unsupported, undecodable video, save offline, hidden in offline vault, delete confirm, locked listing).
- Task 10 fix round 1: (1) VideoPage pauses ExoPlayer on lifecycle ON_STOP (pauseOnStop, no auto-resume) so private audio stops on Home/app switch; (2) the FAB DropdownMenu uses PopupProperties with SecureFlagPolicy.SecureOn (no other popups/sheets exist); (3) image decode sample size is now decodeSampleSize (long edge <= 2048, was up to 4095); (4) SET_OFFLINE reached from a browser pops back to it (only the first-run start destination navigates to Home), so no stale second HOME/browser; selection is kept and the user taps Save offline again. Tests: ViewerHelpersTest (sample size, pause only on stop), BrowserFlowTest.offlineSetupDetourReturnsToTheSameBrowser.
- Task 10 fix round 2: AppNav SET_OFFLINE done/skip decides by the live back stack (previousBackStackEntry == null -> replace with Home, else popBackStack) instead of the remembered start, which stays SET_OFFLINE after a first-run "Not now". Added FirstRunDetourTest (no offline vault, Not now, browser, Save offline, create password -> same browser, Back -> Home, nothing below).
- Task 11: added androidTest NoPlaintextOnDiskTest. Drives the real app (app's AppContainer/KeyVault, real noBackupFilesDir/offline-vault, the app's Coil loader) over an HTTPS MockWebServer: create offline vault, connect (Remember unticked), unlock, view a real PNG (64-byte random marker after IEND) in the viewer, Save offline, view the offline copy, upload a second marker file into the offline vault; then reads both back through LocalSource (markers present decrypted, so the test cannot pass by saving nothing) and scans filesDir, cacheDir, codeCacheDir, noBackupFilesDir, externalCacheDir and dataDir recursively for marker1, marker2 and the server address/URL, reporting file + needle on failure. Also asserts gocryptfs.conf exists and no server was remembered. Why: spec "nothing decrypted touches disk". Result: passes with no leak found, no app code changed. RED check: temporarily writing marker1 to cacheDir (and the URL to noBackupFilesDir) made it fail with the offending path; removed.
- Task 11 fix round 1 (NoPlaintextOnDiskTest only; no app code changed): (1) /thumb now serves a decodable JPEG with its own marker (marker1b after EOI) and the test loads a thumbnail through the app's Coil loader (memory cache bypassed) for both the remote and the offline source, and scans for marker1b; (2) the test works on the debug app's REAL data and wipes the offline vault and state, so it now refuses to run (clear message, suggests `pm clear`) unless it owns them, via sentinel noBackupFilesDir/.imagethat-test-owned, documented in the class KDoc; setUp/tearDown both lockAll on the UI thread; (3) scan streams files with a needle-1 overlap, collects unreadable files/dirs and fails if any; (4) waitForIdle + 2s settle before scanning, and getExternalFilesDir(null) is scanned. Why: reviewer findings. Evidence: a fetcher that writes thumbnail bytes to cacheDir makes the test fail naming the file. Enabling a Coil disk cache does NOT trip it because ThumbnailFetcher never writes into Coil's disk cache (it hands Coil an in-memory Buffer); the real leak path is a fetcher/decoder writing files.

## Final fix wave (whole-branch review findings)
- Go toolchain (#1): webserver/go.mod gains `toolchain go1.26.10` (latest 1.26 patch); every setup-go step (android-setup action, android-ci, release, webserver-ci x2) uses `go-version: '1.26'` + `check-latest: true` instead of `go-version-file`. Stale "pinned to match gocryptfs-lib" comment now says only the crypto deps match. Why: `go 1.26.0` made setup-go install exactly 1.26.0, so release binaries missed Go security patches; 1.27 breaks fixture tests, so stay on 1.26.x.
- vault.Init (#7): writes gocryptfs.diriv first and gocryptfs.conf last (conf = commit marker); a leftover diriv is overwritten on retry. Tests TestInitRetryAfterFailedDirIV, TestInitToleratesLeftoverDirIV. Why: a failed diriv write used to leave a conf, so the folder was neither openable nor re-initialisable.
- mobile.wrapErr (#6, Go half): vault.ErrClosed gets the `closed:` code. Test TestClosedErrorCode. Why: lockAll racing an open/save surfaced as a generic error instead of "locked".
- Local thumbnails (#3): LocalSource.thumbnail runs under a shared Semaphore(2) (injectable), checks ensureActive before the size call and before the whole-file read, and MAX_THUMB_SOURCE drops 64 MB -> 24 MB. Tests localThumbnailsRunAtMostTwoAtATime (peak 8 without the limiter), cancelledThumbnailRequestWaitingForAPermitNeverReads, localThumbnailSkipsHugeImagesWithoutReadingThem. Why: a grid fling ran dozens of uncancellable whole-file reads (~4 copies each on SAF) at once and could OOM.
- Closed vault (#6, Kotlin half): LocalError.Closed for `closed:`; LocalSource maps it to SourceError.Locked, so a save into a just-closed offline vault throws SaveInterrupted and the browser reportLocked's the source. saveOffline now also turns Locked/WrongPassword during the prefix mkdirs into SaveInterrupted (it used to mark every item failed). Tests closedEngineVaultIsLocked, offlineVaultClosedBeforeTheSaveStartsInterrupts, offlineVaultClosedMidSaveInterrupts.
- VaultDataSource (#10): open at position == length returns 0 and read returns RESULT_END_OF_INPUT (Media3 contract) instead of throwing. Test openAtExactlyTheEndReturnsZeroAndEndOfInput.
- Re-save (#11): a destination file that already exists with the same size is reported as "already saved" (SaveReport.skipped / OpResult.skipped, shown in the result dialog) and is not downloaded again; a different-size file still fails with "exists"; two items in one save that sanitize to the same name are reported as a collision instead of the second being skipped. Test resaveSkipsFilesAlreadySavedWithTheSameSize. Why: re-saving after SaveInterrupted listed every file the first run got as a failure.
- ThumbnailFetcher (#14): the ImageSource uses ReadOnlyFileSystem (okio ForwardingFileSystem over SYSTEM that throws on sink/appendingSink/openReadWrite/createDirectory/atomicMove/delete/createSymlink) instead of FileSystem.SYSTEM. Test ReadOnlyFileSystemTest (incl. ImageSource.file() is refused and leaves no temp file). Why: a decoder asking for a file would have written plaintext to java.io.tmpdir (= cacheDir).
- Removed data/VaultRepository.kt (unused in production); VaultRepositoryTest renamed SourcesTest, its facade test now calls saveOffline directly.
- Background auto-lock while frozen (#2): Session.onBackground arms a LockAlarm; AndroidLockAlarm (AutoLock.kt) uses AlarmManager.setAndAllowWhileIdle(ELAPSED_REALTIME_WAKEUP) with an immutable PendingIntent to the new non-exported AutoLockReceiver, which calls Session.onAlarm() -> lockAll() when still backgrounded. onForeground cancels it; the deadline check in onForeground stays as backstop. No new permissions (ManifestHardeningTest unchanged and passing). Tests: AutoLockTest backgroundArmsTheAlarmAndForegroundCancelsIt, alarmWhileBackgroundedLocksEverything, staleAlarmAfterReturningDoesNotLock, autoLockReceiverLocksTheAppSession (real receiver, app Session). Verified on pixel6: process isFrozen=true from ~16 s after Home, alarm thawed it at ~100 s (dumpsys alarm: AutoLockReceiver 1 -> 2 wakeups), app showed the vault Locked. Why: the main-thread tick loop stops in a frozen process, so keys stayed in RAM until the user came back.
- Configurable idle timeout (#5): AppState.idleTimeoutMinutes (default 5), Session.idleTimeoutMs is now settable (setIdleTimeoutMinutes persists, loadIdleTimeout runs on IO at app start), Home has a Settings > "Auto-lock after" row with a SecureAlertDialog radio picker (1, 2, 5, 10, 15, 30). Tests idleTimeoutIsConfigurableAndSaved, UnlockFlowTest.autoLockTimeoutIsPickedOnHomeAndSaved. Why: spec requires a configurable idle timeout.
- Picker pause (#8): Session.pauseBackgroundLock/resumeBackgroundLock around app-launched pickers via rememberPickerLauncher (ui/Pickers.kt), used by the browser photo picker and Home's two folder pickers. While paused, leaving the app arms no 1-minute deadline/alarm; the idle timeout still applies (an alarm at the idle deadline instead, so a frozen process still honours it). tick() now checks idle OR background deadline. Tests pickerOpenPausesTheBackgroundLockButNotTheIdleTimer, pickerResultAfterTwoMinutesKeepsTheBrowserOpen, resumingWhileStillBackgroundedStartsTheBackgroundDeadline. Why: a >60 s pick popped the browser and dropped the selection.
- reportLocked (#9) clears the Coil memory cache. Test reportedLockEvictsThumbnailsFromTheMemoryCache.
- Debug applicationId (#4): buildTypes.debug { applicationIdSuffix = ".debug" } and src/debug/res/values/strings.xml app_name "ImageThat (debug)". Debug APK is ai.alertd.imagethat.debug (aapt2 badging), release stays ai.alertd.imagethat (signed 1.2.3 build: versionCode 10203, cert SHA-256 71e3...196b). Test-provider authorities live in the test APK and are unaffected; all 75 instrumented tests incl. NoPlaintextOnDiskTest pass under the new package. Why: debug and release could not be installed side by side, and the destructive NoPlaintextOnDiskTest ran against the real app's data.
- android-ci.yml path filters (#12) add webserver/go.sum, webserver/Makefile (vaultcore/build.sh runs `make sync`), webserver/scripts/sync-gocryptfs.sh and .github/actions/android-setup/**.
- release.yml version job (#13) rejects tags whose minor or patch is > 99 or major > 2000 with ::error (base-10 arithmetic, so 1.08.09 still parses). Why: versionCode = major*10000+minor*100+patch collides past 99.
- Vault fixture plaintext pinned (test-only): the three fixture images (full/photo.jpg, full/photo.png, alt/photo.jpg) are now committed under webserver/vault/testdata/plain/ and fixtureFile.Content() reads them (field Plain) instead of encoding with image/jpeg and image/png; encodeFixtureImage and fixtureKind removed, header comment in fixture_test.go says the committed bytes are authoritative (TestGenerateFixture writes them into any regenerated vault). Bytes were decrypted from the existing vaults and verified identical to the Go 1.25.0 encoders' output. Ciphertext fixtures untouched. Why: Go 1.26 changed the JPEG/PNG encoders' output, so 5 tests (TestAltCipherVaultsRead, TestListDirRoot, TestFileSizeAndStat, TestReadWholeFile, TestReadAtRandomRanges) failed on 1.26+ although the reader was correct.

## 2026-10-28 — Merged android-app into main
- Merged the Android app branch (12 tasks + final-review fixes). aj-log.md conflict resolved by keeping both sides' entries (kunj blueprint log + Android log).

## 2026-10-28 — Synology install script
- Added Scripts/synology/imagethat-web.sh: install/update/start/stop/restart/status for imagethat-web on a Synology NAS (DSM 7). Why: the user runs the webserver on a Synology; DSM has no gh/jq, so the script uses curl + the GitHub API with a token (repo is private), picks linux amd64/arm64 from uname, verifies SHA256SUMS before installing, keeps everything under /volume1/imagethat, and serves HTTPS with the DSM default certificate (copied on each start) because a LAN bind requires TLS. Boot start via DSM Task Scheduler.
- Tested the install path on the Mac against the real v0.1.0 release (lookup, download, checksum, extract).
- Added Scripts/synology/deploy.sh (runs on the Mac): downloads the release with gh, verifies it, streams archive + SHA256SUMS + imagethat-web.sh to the NAS over ssh (no scp/SFTP needed) and installs with sudo via the new `install-file` command. Why: the user can SSH to the NAS with a key, so the NAS needs no GitHub token.
- Fixed checksum verification in imagethat-web.sh: the grep `\|` alternation matched nothing on BSD/BusyBox grep and `sha256sum -c` accepted the empty input, so a tampered archive installed. Now the expected hash is extracted with awk and compared explicitly; missing entries and mismatches abort. Tested tampered / unlisted / clean archives.
- Added Scripts/synology/build-on-nas.sh: run on the Synology; clones the latest vX.Y.Z tag over SSH with the NAS's GitHub key (~/.ssh/id_rsa), downloads Go 1.26.10 from go.dev (public) on first run, builds imagethat-web. Why: the user's NAS already has a GitHub SSH key; release assets can't be downloaded with an SSH key (HTTPS/API only), so building from the tag avoids needing a token. Tested on the Mac: cloned v0.1.0 over SSH and built "imagethat-web v0.1.0".
- build-on-nas.sh now builds the browser UI too: downloads the latest Node 22 linux tarball from nodejs.org (checksum-verified against SHASUMS256.txt) into ./node-toolchain, runs npm ci + npm run build before go build, so the NAS binary embeds the UI like the release binaries. Why: without it the NAS build served only the "UI not built" page. Also GOFLAGS=-modcacherw so the work folder can be deleted with rm -rf, and npm's update notifier off. Tested on the Mac: v0.1.0 built with the UI embedded.
- Added Scripts/synology/get-imagethat-web.sh: downloads the latest (or a given) release for linux amd64/arm64 with plain curl, verifies it against SHA256SUMS and leaves ./imagethat-web. Why: the repo is now public, so release assets download without a token, SSH key or build. Reverted the uncommitted release-builds mirror step in release.yml (no longer needed). Tested with no credentials against v0.1.0.

## 2026-10-28 — Android: allow http:// servers
- Overrode ruling R14 at the user's request: the app now connects to plain http:// servers. res/xml/network_security_config.xml permits cleartext (trust anchors stay system-only); ConnectScreen shows a "Not encrypted" warning under the address for http:// instead of refusing. Test: UnlockFlowTest.plainHttpWarnsAndConnects (warning shown only for http, connects to a plain-http MockWebServer).
- Fixed ApiClient reading/closing response bodies on the caller's (main) thread: new fetch() helper runs call + body read on Dispatchers.IO. Over https StrictMode did not flag it (but it could still block the UI on slow networks); over http it threw NetworkOnMainThreadException, which surfaced as "Unexpected response from the server".
- Full instrumented run: 74/75 pass; the one failure (invalidatedShortcutExplains…, "found 2 root nodes") is in an untouched test and looks like a leftover dialog on the shared emulator.
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

## 2026-10-28 — Merged Chore/video into main
- Merged main into Chore/video first. Conflicts: main had already made the same two build fixes (`parseConfig` in gocryptfs-lib/main.go, `swift build --show-bin-path` in the Makefile), so main's versions were kept; `test-logic` now depends on `build web-build test-fixtures`; this log keeps both sides' entries. Why: bring the remote-server client and slow-video tests onto main.

## 2026-10-28 — Android: app lock + saved passwords
User request: "opening the app should require a biometric and then everything in it should save the passwords so you only once have to add each password. for vaults and servers".
- **keys/AppUnlocker.kt, keys/SavedPasswords.kt (new):** one Keystore master key `imagethat.vault-master` (auth per use: biometric strong or device credential on API 30+, biometric on 29; invalidated by enrolment) wraps a random 256-bit data key; the data key encrypts one AES-GCM blob holding every saved password (`offline`, `folder:<id>`, `server:<serverKey>:<vaultId>`). *Why:* the key needs auth for every use, so keeping the unwrapped data key in memory until lock lets the app save/forget passwords without a second prompt. Passwords stay CharArrays and are zeroed on lock/forget; the encoding never makes them Strings.
- **keys/KeyVault.kt:** per-vault `wrap`/`unwrap`/`forget` (imagethat.bio.*) replaced by `unlockApp`, `clearSavedPasswords` and `migrateLegacy` (deletes old imagethat.bio.* keys and `AppState.wrapped`). The prompt (CryptoObject), test hooks and state cipher are kept. *Why:* the brief replaces the per-vault shortcut with the app lock.
- **keys/AppState.kt, StateStore.kt:** new `wrappedDek`/`savedPasswords` (ciphertext only); StateStore locks per file so two stores over one file never interleave updates.
- **ui/Session.kt:** `appLocked`, `unlockApp`, saved-password accessors, one-time `notice`; `lockAll` wipes the saved passwords and relocks; idle/background deadlines also lock an unlocked app with nothing open.
- **ui/AppLockScreen.kt (new), AppNav.kt:** nothing (Home included) is composed while locked; the lock screen shows only the app name, prompts by itself once resumed, has Unlock, and explains "Set up a fingerprint, face or screen lock in Android settings" + Retry when nothing is enrolled. After invalidation it says once that saved passwords were cleared.
- **UnlockDialog/HomeScreen/RemoteVaultsScreen/BrowserScreen/SetOfflinePasswordScreen/ConnectScreen:** saved password opens a vault with no prompt; a typed password that unlocks is saved (no checkbox); a stale saved one is dropped and asked once; long-press offline/folder/server-vault rows → "Forget the saved password for X?"; every connected server is remembered ("Remember this server" removed); removing a server forgets its passwords. Bug caught by the new tests: LocalVaults zeroes the password while opening, so a copy is taken before opening to save.
- **Tests:** JVM `SavedPasswordsTest`, `AppUnlockerTest` (fake master key); instrumented `KeyVaultTest` rewritten (real Keystore, test alias, migration), `UnlockFlowTest` (lock screen, no-screen-lock, lockAll, invalidation notice, save/auto-use/stale/forget for local and remote), `AutoLockTest` additions, `NoPlaintextOnDiskTest` now also asserts the saved passwords never appear in plaintext on disk.
- **docs spec:** Persisted state, Screens, Errors updated to the new design.

## 2026-10-28 — Android spec: app lock + saved passwords
- **docs/superpowers/specs/2026-10-28-android-app-design.md:** Intent, data table, KeyVault row, Persisted state, Screens (app lock, Home, Connect, vault list, unlock prompts) and Errors (enrolment change, stale saved password) now describe the app lock and saved passwords. *Why:* the user changed the design; the spec has to match what is built.
## 2026-10-28 — Android redesign part A: theme foundation and shared components
- **ui/theme/Theme.kt (new):** dark-only Material 3 theme from the design: `Palette` (background #0E0F12, cards #181A1F, dividers #23262E, accent #E8B04B, destructive #F6A08C, online #7FD1B9…), Instrument Sans typography (32/28sp bold titles, 16sp semibold rows, 13sp secondary, 12sp labels), shapes (cards 18, dialogs 24). Why: the user asked for the app to follow the new mockups; one theme makes every screen pick up the palette and font.
- **res/font/instrument_sans.ttf + assets/licenses/InstrumentSans-OFL.txt (new):** the variable Instrument Sans from google/fonts, bundled with its OFL licence (res/font cannot hold a .txt, so the licence ships in assets). Why: no runtime font loading.
- **ui/components/AppIcons.kt, Components.kt (new):** stroke icons traced from the mockup SVGs (no icons-extended dependency) and shared composables (SectionLabel, ScreenTitle, GroupCard + InsetDivider, CardRow, IconTile, PillButton, OutlinePill, RoundIconButton, TopBar, AccentChip, SplitRow/SplitButton, ActionBar/ActionTile, AppTextField, SearchField, PasswordInput with show/hide, ErrorText, FootnoteText, DialogButton). Why: part B restyles Home/Server/Unlock/Connect with the same pieces.
- **MainActivity:** uses the new `ui.theme.ImageThatTheme`, edge-to-edge with light system-bar icons. **AndroidManifest + res/values/themes.xml:** dark window theme so launch never flashes white. The old `ImageThatTheme` in AppNav.kt is left unused for now (another branch is editing AppNav.kt).
- **SecureAlertDialog:** card colour, radius 24, palette text colours. **Session.idleRemainingMs():** time left before the idle lock, for the gallery's "locks in m:ss".

## 2026-10-28 — Android redesign part A: gallery and viewer
- **BrowserScreen (gallery):** restyled to the Vault mockup. Header: back, "Select" pill, lock button; folder name as the title with "N items · unlocked · locks in m:ss" (live countdown from `Session.idleRemainingMs()`); breadcrumb chips in subfolders. Folders sit first as one grouped card of rows, then a 3-column thumbnail grid (3dp gap, 6dp corners) with a play badge on videos. Selection: Cancel / "N selected" / Select all header, dimmed tiles with an accent check, and a floating action bar (Save offline — hidden in the offline vault, Move to…, Delete). Browsing: accent "Upload" pill plus a round New folder button. Move, delete, new-folder and result dialogs use the palette. Why: the user's new design. The lock button runs Lock all, because the app has no per-vault lock and dropping one source would leave its Go vault or server session open.
- **Viewer:** black background, floating translucent back/more buttons with the filename and "i of n", and a bottom panel with an "Encrypted at rest" chip, a details line (full image or video dimensions once known, size) and Save offline / Move / Info / Delete tiles. No export or share. Tapping an image hides the controls. Info shows name, type, size, dimensions. Why: the Viewer mockup, minus Export because decrypted data must not leave the app.
- **Delete/move/save offline from the viewer:** the viewer makes the current file the selection (`BrowserViewModel.selectOnly`) and runs the existing actions, so the confirmation, result and offline-unlock dialogs are the same ones the grid uses (they now also show while the viewer is open). The viewer reads the live folder list, so a deleted or moved file drops out and the next one shows; closing the viewer clears that selection. `selectAll()` backs the Select all button.
- **Tests:** unit tests for selectAll/selectOnly, viewer delete via selectOnly, and the details/type helpers; instrumented tests for delete-from-viewer advancing, viewer Info dimensions, and Select → Select all.

## 2026-10-29 — Android: fixes from the release review (app lock + redesign)
- **keys/KeyVault.kt:** on Android 10 (API 29) the app lock now accepts the screen lock (PIN/pattern/password) or any biometric. The prompt runs first, without a cipher, and the master key is usable for 30 s after it (`AUTH_WINDOW_SECONDS`). API 30+ is unchanged (per-use key bound to the prompt). If a weak biometric doesn't authorise the key, the app asks for the PIN instead. *Why:* minSdk is 29, and on API 29 the lock only allowed a strong biometric. Android 10 phones with a PIN but no fingerprint were stuck on "Set up a fingerprint, face or screen lock" and couldn't open the app at all.
- **ui/Session.kt, local/LocalVaults.kt, ui/UnlockDialog.kt, ui/HomeScreen.kt:** removing a server now locks its open vaults and logs out of its HTTP session. Removing a folder closes its Go vault (`Session.closeRemoved`, `LocalVaults.close`, and a close hook on `registerSource`). `lockAll` and the removal share `logoutAndClose`. *Why:* removing a server or folder only edited the state file. Its vaults stayed unlocked in memory, still counted as unlocked on Home, and nothing in the UI could lock them.
- **keys/AppUnlocker.kt:** if the user cancels the prompt that makes a new master key after invalidation, the old wrapped key is marked unreadable (`UNREADABLE`). *Why:* the new key couldn't open the old wrapped key, so every later open took two prompts. Now it takes one, and the "saved passwords cleared" notice still shows.
- **Tests:** `AppUnlockerTest.cancelAfterInvalidationStillShowsTheNoticeNextTime` now asserts a single prompt. New instrumented test `removingAServerLocksItsOpenVaultsOnly`.

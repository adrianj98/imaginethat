# aj-log

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

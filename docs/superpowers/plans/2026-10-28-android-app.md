# ImageThat for Android Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A sideloaded Android app that browses ImageThat webserver vaults and on-device gocryptfs vaults, writing nothing decrypted to disk.

**Architecture:** The webserver's pure-Go `vault/` package is wrapped by a new `webserver/mobile` package and compiled with gomobile into `vaultcore.aar`. A Kotlin/Compose app uses it for local vaults (app-private and SAF-picked) and talks to the webserver's JSON API for remote vaults. Secrets the user opts to remember are wrapped by Android Keystore keys.

**Tech Stack:** Go 1.24+ / gomobile, Kotlin 2.x, Jetpack Compose (Material 3), OkHttp 4, kotlinx.serialization, Coil 3, Media3 ExoPlayer, androidx.biometric, JUnit 4, MockWebServer, AndroidX Test.

**Spec:** `docs/superpowers/specs/2026-10-28-android-app-design.md`

## Global Constraints

- minSdk 29, targetSdk 35, compileSdk 35; applicationId `ai.alertd.imagethat`.
- Nothing decrypted is ever written to disk: no image/video disk caches, no plaintext temp files, no share/FileProvider/export.
- `FLAG_SECURE` on every window; `android:allowBackup="false"`; `dataExtractionRules` excludes everything.
- Password fields: `KeyboardType.Password`, `autoCorrect = false`.
- Built-in vault lives at `noBackupFilesDir/offline-vault`; persisted state at `noBackupFilesDir/state.bin`.
- Offline vault password: user-chosen, entered twice, minimum 8 characters. Biometrics is only a shortcut.
- Lock everything after the idle timeout (default 5 minutes) and after 1 minute in background.
- Saved copies mirror the remote layout under `/<server label>/<vault name>/…` in the built-in vault.
- `vault/` gains exactly `Init` and `Open`; existing behaviour unchanged.
- Release: `versionCode = major*10000 + minor*100 + patch`; APK named `imagethat-android_vX.Y.Z.apk`; signing secrets `ANDROID_KEYSTORE_BASE64`, `ANDROID_KEYSTORE_PASSWORD`, `ANDROID_KEY_ALIAS`, `ANDROID_KEY_PASSWORD`; job fails if any is missing.
- Every change logged in `aj-log.md` (user rule) and committed per task.

## Review Focus

1. **Multi-GB video saved offline or uploaded** — must stream in bounded memory (≤ a few MiB), never buffer the whole file. Test in Task 2 (64 MiB write through `FileWriter` with a memory ceiling) and Task 8 (`saveOffline` of a large streamed body).
2. **Remote session expires mid-browse** (server idle lock or restart → 401/403) — UI returns to the unlock prompt for that vault instead of showing an empty folder or crashing. Test in Task 6 (`ApiClient` maps 401 to `RemoteError.Locked`) and Task 8 (repository surfaces it).
3. **App killed or save cancelled mid-file** — no truncated ciphertext left that later lists as a corrupt file. Test in Task 2 (`FileWriter.Abort` leaves no entry) and Task 8 (failed item removed).
4. **Desktop-made vaults with other options** (AES-SIV, XChaCha, long names > 175 chars) opened from a SAF folder — open and read correctly. Test in Task 3 using the existing `webserver/vault/testdata/vault-aessiv`, `vault-xchacha` and long-name fixtures through `OpenStorage`.
5. **Fingerprint set changed** — shortcut key invalidated, password still opens the vault, shortcut can be re-enabled. Test in Task 5 (`KeyPermanentlyInvalidatedException` path deletes the wrapped secret and returns `Unwrap.Invalidated`).

---

### Task 1: `vault.Init` and `vault.Open`

**Files:**
- Create: `webserver/vault/init.go`
- Modify: `webserver/vault/session.go` (add `Open`; refactor `OpenLocal` to share config parsing)
- Test: `webserver/vault/init_test.go`

**Interfaces:**
- Produces: `func Init(fs WriteVFS, password string) error`; `func Open(fs VFS, password string) (*Session, error)`.

- [ ] **Step 1: Write failing tests** in `init_test.go`:
  - `TestInitThenOpen`: `Init(NewLocalFS(dir), "pw-123456")`, then `Open(NewLocalFS(dir), "pw-123456")` succeeds, `Create("a.txt", "hello")` then `ReadFile("a.txt") == "hello"`; `Open(..., "wrong")` returns `ErrWrongPassword`.
  - `TestInitRefusesExistingVault`: second `Init` on same dir returns an error wrapping `fs.ErrExist`, original conf unchanged.
  - `TestOpenNoConfig`: `Open` on empty dir returns `ErrNoConfig`.
  - `TestInitInteropWithGocryptfs` (skip under `-short` or when `gocryptfs`/FUSE missing, same guard as `interop_test.go`): mount the `Init`-made vault with the real binary, write a file through the mount, read it back via `Open`.
- [ ] **Step 2:** `cd webserver && go test -tags without_openssl ./vault/ -run 'Init|OpenNoConfig'` → FAIL (undefined `Init`).
- [ ] **Step 3: Implement.** `Init`: refuse if `fs.Exists("gocryptfs.conf")`; call `configfile.Create` with `Filename` in an `os.MkdirTemp` dir, `LogN: configfile.ScryptDefaultLogN`, `Creator: "imagethat"`, defaults otherwise (AES-GCM, encrypted names, long names); read the bytes, `fs.WriteFile("gocryptfs.conf", b)`, remove the temp dir (it only ever holds the encrypted master key); write 16 bytes from `crypto/rand` to `gocryptfs.diriv`. `Open`: `fs.ReadFile("gocryptfs.conf")` → `ErrNoConfig` if missing → `json.Unmarshal` + `cf.Validate()` (same as `parseConfig` in `gocryptfs-lib/main.go`) → `newSession`. `OpenLocal` keeps its permission-error handling and then calls the shared parse.
- [ ] **Step 4:** `go test -tags without_openssl ./vault/...` → all PASS, including pre-existing tests.
- [ ] **Step 5: Commit** `feat(vault): Init and VFS-based Open for mobile`.

### Task 2: `mobile` package — local vaults

**Files:**
- Create: `webserver/mobile/mobile.go`, `webserver/mobile/writer.go`
- Test: `webserver/mobile/mobile_test.go`

**Interfaces:**
- Consumes: `vault.Init`, `vault.Open`, `vault.NewLocalFS`, `Session.ListDir/Open/Create/Mkdir/Move/Remove/Close`.
- Produces (all gomobile-bindable):
  - `func InitLocal(dir, password string) error`
  - `func OpenLocal(dir, password string) (*Vault, error)`
  - `type Vault`; methods `List(path string) (string, error)` (JSON array of `vault.Entry`), `Size(path string) (int64, error)`, `ReadRange(path string, off int64, n int) ([]byte, error)`, `Create(path string) (*FileWriter, error)`, `Mkdir(path string) error`, `Move(from, to string) error`, `Remove(path string) error`, `Close() error`
  - `type FileWriter`; methods `Write(p []byte) error`, `Close() error` (commits), `Abort() error` (removes partial entry)
  - Error strings prefixed with stable codes so Kotlin can map them: `wrong_password:`, `no_config:`, `not_found:`, `exists:`, `invalid_path:`.

- [ ] **Step 1: Write failing tests:** round-trip create/list/read/move/remove on an `InitLocal` vault; `ReadRange` at offsets 0, 4095, 4096 (block boundary) and past EOF (returns short slice, no error); `OpenLocal` wrong password error starts with `wrong_password:`; `TestFileWriterBoundedMemory` writes 64 MiB in 256 KiB chunks and asserts `runtime.MemStats.HeapInuse` growth < 16 MiB; `TestFileWriterAbort` → path absent from `List` afterwards and no leftover temp files in the cipher dir; read the committed fixture `../vault/testdata/vault` via `OpenLocal` with its fixture password.
- [ ] **Step 2:** `go test -tags without_openssl ./mobile/` → FAIL.
- [ ] **Step 3: Implement.** `FileWriter` runs `Session.Create(path, pipeReader)` in a goroutine fed by an `io.Pipe`; `Close` closes the writer and waits for the result; `Abort` closes with an error and calls `Remove` if the entry exists. `ReadRange` uses `Session.Open` + `ReadAt`. Map `vault` sentinel errors to the code prefixes in one helper.
- [ ] **Step 4:** tests PASS.
- [ ] **Step 5: Commit** `feat(mobile): gomobile wrapper for local vaults`.

### Task 3: `mobile` package — `Storage` backends (for SAF)

**Files:**
- Create: `webserver/mobile/storage.go`
- Test: `webserver/mobile/storage_test.go`

**Interfaces:**
- Produces:
  - `type Storage interface { ReadAt(path string, offset int64, n int) ([]byte, error); FileSize(path string) (int64, error); ListDir(path string) (string, error) /* JSON [{name,isDir,size}] */; WriteFile(path string, data []byte) error; OpenWriter(path string) (StorageWriter, error); Mkdir(path string) error; Remove(path string) error; RemoveAll(path string) error; Rename(from, to string) error; Exists(path string) bool }`
  - `type StorageWriter interface { Write(p []byte) error; Close() error }`
  - `func InitStorage(s Storage, password string) error`; `func OpenStorage(s Storage, password string) (*Vault, error)`
  - internal adapter `storageVFS` implementing `vault.WriteVFS` (its `CreateTemp` makes `<dir>/.tmp-<random>` via `OpenWriter`).

- [ ] **Step 1: Write failing tests** with `dirStorage`, a test-only `Storage` over a temp dir that deliberately does *not* implement `vault.FileOpener`: Task 2's round-trip suite run through `OpenStorage`; open `../vault/testdata/vault-aessiv` and `vault-xchacha` (copied to temp dirs) and read every file, comparing to the fixture generator's expected contents; a long-name (> 175 bytes) file can be created, listed and read.
- [ ] **Step 2:** FAIL. **Step 3:** implement the adapter. **Step 4:** PASS.
- [ ] **Step 5: Commit** `feat(mobile): Storage interface for non-filesystem vaults`.

### Task 4: Android project, gomobile build, hardening

**Files:**
- Create: `android/settings.gradle.kts`, `android/build.gradle.kts`, `android/gradle/libs.versions.toml`, `android/gradle.properties`, Gradle wrapper, `android/app/build.gradle.kts`, `android/app/src/main/AndroidManifest.xml`, `android/app/src/main/res/xml/data_extraction_rules.xml`, `android/app/src/main/java/ai/alertd/imagethat/{ImageThatApp.kt,MainActivity.kt}`, `android/vaultcore/build.sh`, `android/.gitignore`
- Modify: root `Makefile` (targets `android-aar`, `android-debug`, `android-test`), `.gitignore`
- Test: `android/app/src/test/java/ai/alertd/imagethat/ManifestHardeningTest.kt`, `android/app/src/androidTest/java/ai/alertd/imagethat/VaultcoreSmokeTest.kt`

**Interfaces:**
- Produces: Gradle task `:app:buildVaultcore` (runs `vaultcore/build.sh` → `android/app/libs/vaultcore.aar`, gitignored) that `preBuild` depends on; Kotlin package `mobile` from the aar.

- [ ] **Step 1: Toolchain on the dev Mac:** `brew install openjdk@21`; `go install golang.org/x/mobile/cmd/gomobile@latest && gomobile init`; `ANDROID_HOME=~/Library/Android/sdk`, NDK 26.1.10909125. Verify `java -version` shows 21 and `gomobile version` runs.
- [ ] **Step 2: Write failing tests:** `ManifestHardeningTest` (Robolectric-free: parse merged manifest from `build/intermediates`) asserts `allowBackup=false`, `dataExtractionRules` set, no `<provider>` elements, `INTERNET` is the only dangerous-or-normal permission besides `USE_BIOMETRIC`; `VaultcoreSmokeTest` (instrumented) calls `Mobile.initLocal(tmp, "pw-123456")`, `Mobile.openLocal`, creates and reads a file.
- [ ] **Step 3: Implement.** `build.sh`: `cd webserver && make sync && gomobile bind -target=android/arm64,android/amd64 -androidapi 29 -tags without_openssl -ldflags=-s -o ../android/app/libs/vaultcore.aar ./mobile`. `MainActivity` sets `FLAG_SECURE` before `setContent`. Coil singleton configured with `diskCachePolicy(DISABLED)`, memory cache only.
- [ ] **Step 4:** `make android-test` (`./gradlew :app:testDebugUnitTest`) PASS; `./gradlew :app:connectedDebugAndroidTest` PASS on an API 34 emulator; `make android-debug` produces `app-debug.apk`.
- [ ] **Step 5: Commit** `feat(android): project skeleton with gomobile vault engine`.

### Task 5: Keys and encrypted state

**Files:**
- Create: `android/app/src/main/java/ai/alertd/imagethat/keys/{KeyVault.kt,StateStore.kt,AppState.kt}`
- Test: `android/app/src/test/.../keys/StateStoreTest.kt`, `android/app/src/androidTest/.../keys/KeyVaultTest.kt`

**Interfaces:**
- Produces:
  - `interface SecretCipher { fun encrypt(plain: ByteArray): ByteArray; fun decrypt(blob: ByteArray): ByteArray }`
  - `class KeyVault(context)`: `fun stateCipher(): SecretCipher` (non-biometric key alias `imagethat.state`); `suspend fun wrap(activity: FragmentActivity, id: String, secret: ByteArray)`; `suspend fun unwrap(activity, id): Unwrap` where `sealed interface Unwrap { data class Ok(val secret: ByteArray); object Invalidated; object Cancelled; object None }`; `fun forget(id: String)`. Biometric keys: alias `imagethat.bio.<id>`, `setUserAuthenticationRequired(true)`, `BIOMETRIC_STRONG`, `setInvalidatedByBiometricEnrollment(true)`.
  - `@Serializable data class AppState(val servers: List<SavedServer> = emptyList(), val folders: List<SavedFolder> = emptyList(), val wrapped: Map<String, String> = emptyMap() /* id -> base64 */, val offlineVaultCreated: Boolean = false)`; `SavedServer(id, label, url, pinnedSha256: String?)`; `SavedFolder(id, label, treeUri)`.
  - `class StateStore(file: File, cipher: SecretCipher)`: `fun load(): AppState` (missing file → default), `fun update(block: (AppState) -> AppState)`.

- [ ] **Step 1: Failing tests:** `StateStoreTest` with an XOR fake cipher: round-trip; file bytes do not contain the server URL in plaintext; corrupted file → default state and the corrupt file is renamed `state.bin.corrupt` (not silently deleted). `KeyVaultTest` (instrumented, no biometric enrolled on emulator → use a test hook `KeyVault(context, requireAuth = false)`): wrap/unwrap round-trip; deleting the Keystore entry then `unwrap` → `Unwrap.Invalidated` and the wrapped secret is removed from state.
- [ ] **Step 2:** FAIL. **Step 3:** implement (`KeyPermanentlyInvalidatedException` → `Invalidated`). **Step 4:** PASS.
- [ ] **Step 5: Commit** `feat(android): Keystore-wrapped secrets and encrypted state`.

### Task 6: Remote API client

**Files:**
- Create: `android/app/src/main/java/ai/alertd/imagethat/remote/{ApiClient.kt,ApiModels.kt,CertPinning.kt}`
- Test: `android/app/src/test/.../remote/ApiClientTest.kt`

**Interfaces:**
- Produces:
  - `class ApiClient(baseUrl: HttpUrl, pinnedSha256: String?)` with an in-memory `CookieJar`.
  - `suspend fun vaults(): List<RemoteVault>`; `unlock(id, password: CharArray)`; `lock(id)`; `list(id, path): Listing`; `openFile(id, path, range: LongRange?): Response` (caller closes); `thumb(id, path, size: Int): ByteArray`; `upload(id, dir, name, body: RequestBody)`; `mkdir(id, path)`; `move(id, from, to)`; `delete(id, path)`; `logout()`; `fun close()` (clears cookies, evicts connection pool).
  - Models mirror server JSON: `RemoteVault(id, name, unlocked)`, `Listing(path, parent: String?, entries: List<Entry>)`, `Entry(name, isDir, size, mediaType)`.
  - `sealed class RemoteError : Exception { Locked, WrongPassword, RateLimited, NotFound, CertMismatch(val sha256: String), Network(cause) }`.
  - `suspend fun probeCertificate(url): String?` → SHA-256 of the leaf cert (for the Connect screen).

- [ ] **Step 1: Failing tests** against `MockWebServer`: each endpoint sends the right method/path/query (`path`, `name`, `size`, `Range` header) and parses the fixture JSON; cookie from `/unlock` is sent on later calls and gone after `close()`; 401 and 403 → `Locked`, 429 → `RateLimited`, wrong-password response → `WrongPassword`; HTTPS MockWebServer with a self-signed cert: pinned correct hash works, wrong hash → `CertMismatch` carrying the actual hash; the `OkHttpClient` has no `Cache`.
- [ ] **Step 2:** FAIL. **Step 3:** implement (read server status codes/JSON from `webserver/server/handlers_fs.go` and `handlers_media.go`). **Step 4:** PASS.
- [ ] **Step 5: Commit** `feat(android): webserver API client`.

### Task 7: Local vaults and SAF storage

**Files:**
- Create: `android/app/src/main/java/ai/alertd/imagethat/local/{LocalVaults.kt,SafStorage.kt}`
- Test: `android/app/src/androidTest/.../local/{LocalVaultsTest.kt,SafStorageTest.kt}`

**Interfaces:**
- Consumes: `mobile.Mobile`, `mobile.Vault`, `mobile.Storage`, `KeyVault`, `StateStore`.
- Produces:
  - `class SafStorage(resolver: ContentResolver, treeUri: Uri) : mobile.Storage` (vault-relative paths resolved to document IDs; child lookups cached per directory for the session only).
  - `class LocalVaults(context, state: StateStore)`: `fun offlineExists(): Boolean`; `fun createOffline(password: CharArray)` (rejects < 8 chars with `IllegalArgumentException`); `fun openOffline(password: CharArray): mobile.Vault`; `fun openFolder(folder: SavedFolder, password: CharArray): mobile.Vault`; `fun createInFolder(treeUri, password)`; `fun closeAll()`.

- [ ] **Step 1: Failing tests:** offline create → open → write → reopen after `closeAll`; wrong password maps to a typed `LocalError.WrongPassword`; `createOffline("short")` throws; `SafStorageTest` using a `DocumentsProvider`-backed temp tree (AndroidX test `DocumentFile` on `cacheDir` via a test provider) runs open/list/read/write/rename/delete and opens the AES-SIV fixture copied into it; after `releasePersistableUriPermission`, `openFolder` throws `LocalError.Unavailable` (UI shows "Pick again").
- [ ] **Step 2:** FAIL. **Step 3:** implement. **Step 4:** PASS.
- [ ] **Step 5: Commit** `feat(android): built-in offline vault and SAF-backed vaults`.

### Task 8: `VaultRepository`

**Files:**
- Create: `android/app/src/main/java/ai/alertd/imagethat/data/{VaultRepository.kt,Sources.kt,SaveOffline.kt}`
- Test: `android/app/src/test/.../data/VaultRepositoryTest.kt`

**Interfaces:**
- Consumes: `ApiClient`, `mobile.Vault` (behind an interface for fakes).
- Produces:
  - `interface VaultSource { suspend fun list(path: String): List<Item>; suspend fun open(path: String, range: LongRange? = null): InputStream; suspend fun size(path: String): Long; suspend fun thumbnail(path: String, px: Int): ByteArray; suspend fun upload(dir: String, name: String, input: InputStream, length: Long); suspend fun mkdir(path: String); suspend fun move(from: String, to: String); suspend fun delete(path: String) }` with `RemoteSource(api, vaultId)` and `LocalSource(vault)` (local thumbnails decoded/downscaled in memory with `BitmapFactory` `inSampleSize`).
  - `data class Item(val path: String, val name: String, val isDir: Boolean, val size: Long, val media: MediaKind)`; `enum class MediaKind { IMAGE, VIDEO, OTHER }`.
  - `suspend fun saveOffline(from: VaultSource, items: List<Item>, into: LocalSource, prefix: String): SaveReport` — recursive for folders; streams in 256 KiB chunks; `SaveReport(saved: List<String>, failed: Map<String, String>)`; failed files removed from `into`.
  - `sealed class SourceError { Locked; WrongPassword; NotFound; Other(msg) }` unified from remote and local errors.

- [ ] **Step 1: Failing tests** with fake sources: listing maps media kinds; `saveOffline` of a folder tree reproduces names and bytes under `"<server>/<vault>/"`; a source that fails mid-stream on one file → that file in `failed`, absent from destination, others saved; a 64 MiB streamed item saves with heap growth < 16 MiB; remote `Locked` surfaces as `SourceError.Locked`; upload passes length through.
- [ ] **Step 2:** FAIL. **Step 3:** implement. **Step 4:** PASS.
- [ ] **Step 5: Commit** `feat(android): unified vault repository and save-offline`.

### Task 9: Screens — home, connect, unlock, auto-lock

**Files:**
- Create: `android/app/src/main/java/ai/alertd/imagethat/ui/{AppNav.kt,HomeScreen.kt,ConnectScreen.kt,RemoteVaultsScreen.kt,UnlockDialog.kt,SetOfflinePasswordScreen.kt,Session.kt}`
- Test: `android/app/src/androidTest/.../ui/{UnlockFlowTest.kt,AutoLockTest.kt}`

**Interfaces:**
- Consumes: `StateStore`, `KeyVault`, `LocalVaults`, `ApiClient`, `VaultRepository`.
- Produces: `class Session` (app-scoped holder of open `VaultSource`s and `ApiClient`s) with `fun lockAll()` (closes Go vaults, `ApiClient.close()`, clears Coil memory cache); `ProcessLifecycleOwner` observer calls it after 1 minute in background; idle timer (default 5 minutes, any touch resets) calls it.

- [ ] **Step 1: Failing Compose UI tests:** first launch with no offline vault shows *Set offline password*, mismatch or < 8 chars disables *Create*; unlock dialog wrong password shows inline error and keeps dialog; with shortcut enabled and `Unwrap.Invalidated`, dialog explains fingerprints changed and asks for the password, then offers to re-enable; "Remember this server" unticked → `StateStore` unchanged after connecting; `AutoLockTest` advances a fake clock past 5 minutes → `Session` has no open sources.
- [ ] **Step 2:** FAIL. **Step 3:** implement screens per spec "Screens" 1–3 and 6. **Step 4:** PASS.
- [ ] **Step 5: Commit** `feat(android): home, connect, unlock and auto-lock`.

### Task 10: Screens — browser and viewer

**Files:**
- Create: `android/app/src/main/java/ai/alertd/imagethat/ui/{BrowserScreen.kt,ViewerScreen.kt,BrowserViewModel.kt}`, `android/app/src/main/java/ai/alertd/imagethat/media/{VaultDataSource.kt,ThumbnailFetcher.kt}`
- Test: `android/app/src/test/.../ui/BrowserViewModelTest.kt`, `android/app/src/androidTest/.../media/VaultDataSourceTest.kt`

**Interfaces:**
- Consumes: `VaultSource`, `saveOffline`, `Session`.
- Produces: `VaultDataSource(source: VaultSource) : androidx.media3.datasource.DataSource` honouring `DataSpec.position/length`; remote video uses `OkHttpDataSource.Factory` on the session's `OkHttpClient`; Coil `Fetcher` for `vault://<sourceId>/<path>?thumb=<px>` keys.

- [ ] **Step 1: Failing tests:** `BrowserViewModelTest` — selection, *Save offline* produces a report shown as a per-file result list, *Delete* asks for confirmation then refreshes, `SourceError.Locked` emits a navigate-to-unlock event; *Save offline* hidden when browsing the offline vault itself; `VaultDataSourceTest` reads ranges `[0,100)`, `[4090,4200)` and to EOF from a local vault file and matches the plaintext.
- [ ] **Step 2:** FAIL. **Step 3:** implement grid, breadcrumb, multi-select bar, FAB (system photo picker → `upload`, *New folder*), move (folder picker within the same vault), viewer pager with pinch zoom and Media3 player. **Step 4:** PASS.
- [ ] **Step 5: Commit** `feat(android): browser and viewer`.

### Task 11: No-plaintext-on-disk test

**Files:**
- Test: `android/app/src/androidTest/.../NoPlaintextOnDiskTest.kt`

- [ ] **Step 1: Write the test:** start an in-process `MockWebServer` serving a vault listing and a file whose bytes contain a 64-byte random marker; open it in the viewer, save it offline, view the offline copy, upload a file containing a second marker into the offline vault; then walk `filesDir`, `cacheDir`, `codeCacheDir`, `noBackupFilesDir`, `externalCacheDir` and assert no file contains either marker or the server URL; assert `noBackupFilesDir/offline-vault` contains `gocryptfs.conf`.
- [ ] **Step 2:** run `./gradlew :app:connectedDebugAndroidTest --tests '*NoPlaintextOnDisk*'` → PASS (fix any leak found in the owning task's code, not in the test).
- [ ] **Step 3: Commit** `test(android): assert nothing decrypted reaches disk`.

### Task 12: CI, release job, signing key

**Files:**
- Create: `.github/workflows/android-ci.yml`
- Modify: `.github/workflows/release.yml` (add `android` job; `publish.needs` gains `android`; notes gain an Android section), `android/app/build.gradle.kts` (release `signingConfig` from env), `README.md` (Android section)

**Interfaces:**
- Consumes: Gradle `assembleRelease` reading `ANDROID_KEYSTORE_FILE`, `ANDROID_KEYSTORE_PASSWORD`, `ANDROID_KEY_ALIAS`, `ANDROID_KEY_PASSWORD`, `VERSION_NAME` env vars.

- [ ] **Step 1:** `android-ci.yml` on PRs and pushes to `main` touching `android/**`, `webserver/vault/**`, `webserver/mobile/**`: setup Go, JDK 21, Android SDK + NDK 26.1.10909125, gomobile; `go test -short ./vault/... ./mobile/...`; `./gradlew :app:testDebugUnitTest :app:assembleDebug`; instrumented tests on `reactivecircus/android-emulator-runner` API 34.
- [ ] **Step 2:** `release.yml` `android` job: same setup, fail fast if any of the 4 secrets is empty, decode keystore to `$RUNNER_TEMP`, `VERSION_NAME=${version}` → `./gradlew :app:assembleRelease`, verify with `apksigner verify`, rename to `imagethat-android_v${version}.apk`, upload artifact `android`.
- [ ] **Step 3: Generate the signing key** (with the user's go-ahead in-session): `keytool -genkeypair -v -keystore ~/imagethat-release.jks -alias imagethat -keyalg RSA -keysize 4096 -validity 10950` with random passwords; `gh secret set` the 4 secrets; write the passwords to `~/imagethat-release-keystore.txt` (chmod 600) and tell the user to back up both files. Never commit either.
- [ ] **Step 4: Verify:** `actionlint` (or YAML parse) on both workflows; locally `VERSION_NAME=0.0.0 ANDROID_KEYSTORE_FILE=~/imagethat-release.jks … ./gradlew :app:assembleRelease` and `apksigner verify --print-certs` shows the new cert; `versionCode` for `1.2.3` is `10203`.
- [ ] **Step 5: Commit** `ci(android): CI workflow and signed APK in tag releases`.

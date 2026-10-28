# ImageThat for Android — design

Date: 2026-10-28
Status: draft, awaiting review

## Intent

**What the user asked for**

- An Android app that connects to the ImageThat webserver to browse vaults.
- It saves nothing on the phone unless the user selects it, and anything saved
  is stored encrypted.
- It uses gocryptfs on the phone, both to store saved copies and to open
  vaults that already live on the device.
- v1 covers: browse + view images, play videos, save for offline, upload and
  manage (mkdir, move, delete).
- Saved copies are for offline viewing inside the app only — never exported to
  the gallery or shared storage.
- Offline copies unlock with biometrics via the Android Keystore.
- Local vaults: both a built-in offline vault in app-private storage and
  vaults the user picks from phone storage (SD card, synced folders).
- An APK is built on release. Releases happen only when a `vX.Y.Z` tag is
  pushed, and one tag releases both the webserver and the APK.

**Assumptions** (correct any of these on review)

- Single user, sideloaded APK from GitHub Releases; no Play Store in v1.
- Remote vaults are decrypted by the server, as the web UI does today. The
  phone never sees a remote vault's password beyond sending it to `/unlock`.
- Success = every v1 feature works against a real webserver and real gocryptfs
  vaults, and an audit of the app's files directory after a session without
  saves shows nothing but the empty built-in vault.

## Principle: nothing decrypted touches disk

This mirrors the rule in `CLAUDE.md` for the macOS app. Concretely, on the
phone:

| Data | Where it lives |
|---|---|
| Remote images, thumbnails, video bytes | Memory only; image loader and player disk caches disabled |
| Remote session cookie | In-memory cookie jar; gone when the process dies |
| Server address | Memory only, unless the user ticks "Remember this server" — then in the encrypted app store (below) |
| Saved-for-offline files | Inside the built-in gocryptfs vault (ciphertext only) |
| Local vault passwords | Never stored, unless "Unlock with biometrics" is chosen — then wrapped by a biometric-gated Keystore key |
| Built-in vault password | 32 random bytes, wrapped by a biometric-gated Keystore key |
| Temp files during writes | gocryptfs ciphertext only (the Go engine streams through `CreateTemp`) |

Android-specific leak points closed in v1:

- `FLAG_SECURE` on every window: no screenshots, no recents-screen thumbnail.
- `android:allowBackup="false"` and empty `dataExtractionRules`: nothing goes to
  Google backup or device-to-device transfer.
- No `FileProvider`, no share intents, no "open with": decrypted bytes never
  leave the process.
- Password fields use `KeyboardType.Password` with `autoCorrect = false` so
  keyboards do not learn them.
- Coil memory cache only (`diskCachePolicy = DISABLED`); Media3/ExoPlayer with
  no `CacheDataSource`.
- The app locks everything (closes Go sessions, wipes keys, clears image
  memory cache, drops cookies) after a configurable idle timeout and when the
  process is backgrounded for longer than 1 minute.

## Architecture

```
android/                         Gradle project (Kotlin, Jetpack Compose)
  app/                           UI + Android glue
  settings.gradle.kts, build.gradle.kts, gradle/libs.versions.toml
webserver/vault/                 shared gocryptfs implementation (+ new Init)
webserver/mobile/                new: gomobile-friendly wrapper over vault/
.github/workflows/android-ci.yml build + tests on PRs and main
.github/workflows/release.yml    gains an `android` job
```

- **minSdk 29** (Android 10), targetSdk 35, Kotlin 2.x, Compose BOM, Material 3.
- `webserver/mobile` lives in the webserver Go module so it imports `vault/`
  and reuses the generated `internal/gocryptfs`. `gomobile bind
  -target=android -androidapi 29` produces `vaultcore.aar`, which a Gradle task
  builds before `app` compiles (and CI builds the same way).

### Go side

**`vault.Init(fs WriteVFS, password string) error`** — the only addition to
`vault/`. Writes `gocryptfs.conf` and the root `gocryptfs.diriv` using
gocryptfs' own `configfile.Create` with gocryptfs defaults (AES-256-GCM,
encrypted names, long names, DirIV, HKDF, raw64). A test creates a vault with
`Init` and opens it with the real `gocryptfs` binary (skipped under `-short`,
as the existing interop tests are).

**`mobile` package** — gomobile can only pass primitives, `[]byte`, strings
and interfaces, so it wraps `vault.Session`:

```go
type Storage interface {          // implemented in Kotlin for SAF folders
    ReadAt(path string, offset int64, n int) ([]byte, error)
    FileSize(path string) (int64, error)
    ListDir(path string) (string, error)   // JSON [{name,isDir,size}]
    WriteFile(path string, data []byte) error
    OpenWriter(path string) (Writer, error)
    Mkdir(path string) error
    Remove(path string) error
    Rename(from, to string) error
    Exists(path string) bool
}

func InitLocal(dir, password string) error
func InitStorage(s Storage, password string) error
func OpenLocal(dir, password string) (*Vault, error)
func OpenStorage(s Storage, password string) (*Vault, error)

func (v *Vault) List(path string) (string, error)          // JSON []vault.Entry
func (v *Vault) Size(path string) (int64, error)
func (v *Vault) ReadRange(path string, off int64, n int) ([]byte, error)
func (v *Vault) Create(path string) (*FileWriter, error)    // Write([]byte), Close()
func (v *Vault) Mkdir(path string) error
func (v *Vault) Move(from, to string) error
func (v *Vault) Remove(path string) error
func (v *Vault) Close() error                               // wipes keys
```

`FileWriter` feeds an `io.Pipe` into `Session.Create`, so Kotlin streams
plaintext in chunks and only ciphertext is written. Password strings are
copied into a `[]byte` and wiped after key derivation.

`OpenLocal`/`InitLocal` serve the built-in vault under
`context.noBackupFilesDir/offline-vault` (plain file paths, fastest).
`OpenStorage` serves SAF folders via a Kotlin `Storage` built on
`DocumentsContract`.

### Kotlin side

| Unit | Job | Depends on |
|---|---|---|
| `remote/ApiClient` | OkHttp client for the webserver JSON API (`/api/vaults`, `unlock`, `lock`, `list`, `file`, `thumb`, `upload`, `mkdir`, `move`, `entry`, `logout`); in-memory cookie jar; optional pinned cert | OkHttp, kotlinx.serialization |
| `local/LocalVaults` | Opens/creates vaults via `vaultcore`; owns the built-in vault | `vaultcore.aar`, `keys` |
| `local/SafStorage` | `mobile.Storage` over a persisted SAF tree URI | `DocumentsContract` |
| `keys/KeyVault` | Keystore AES-GCM key with `setUserAuthenticationRequired(true)`, biometric-strong; wraps/unwraps secrets (built-in vault password, remembered local vault passwords, remembered servers) | `androidx.biometric` |
| `data/VaultRepository` | One interface for UI: `list`, `open stream`, `thumbnail`, `upload`, `mkdir`, `move`, `delete`, `saveOffline` — backed by a remote or local source | the above |
| `media/VaultDataSource` | Media3 `DataSource` that reads ranges from a local vault; remote video uses OkHttp data source with the session cookie | Media3 |
| `ui/*` | Compose screens | `VaultRepository` |

`VaultRepository.saveOffline(remoteItem)` streams `GET /file` straight into
`LocalVaults.builtin.create(path)`; a folder is saved recursively. The built-in
vault mirrors the remote layout under `/<server label>/<vault name>/…`.

### Persisted state

One file, `noBackupFilesDir/state.bin`, AES-GCM encrypted with a
non-biometric Keystore key (so the app can list what exists before the user
authenticates). It holds only what the user opted into: remembered servers
(URL, label, pinned cert hash), picked SAF folder URIs, and wrapped secrets.
Wrapped secrets inside it still need biometrics to unwrap. Nothing else is
written.

## Screens

1. **Home** — three sections: *Servers* (remembered + "Connect…"), *On this
   phone* (Offline vault, picked folders, "Open folder…", "New vault…"),
   lock-all button.
2. **Connect** — URL, optional "trust this certificate" (shows SHA-256 of a
   self-signed cert on first contact), "Remember this server" checkbox.
3. **Vault list** (server) — the server's vaults with lock state; tap to
   unlock with password.
4. **Browser** — grid of thumbnails + folders; breadcrumb; multi-select bar:
   *Save offline*, *Move*, *Delete*; FAB: *Upload* (system photo picker),
   *New folder*.
5. **Viewer** — swipeable full-screen images (pinch zoom) and video (Media3
   player with seek).
6. **Unlock prompts** — password dialog with "Unlock with biometrics next
   time" for local vaults; biometric prompt for the offline vault.

## Errors

- Wrong password → inline error; remote 429 → "Too many attempts, wait".
- Server unreachable/TLS mismatch → banner on the server; pinned-cert
  mismatch is a hard failure with the new fingerprint shown.
- Remote session expired (401/locked) → back to unlock prompt for that vault.
- Biometric enrolment changed → Keystore key invalidated → offline vault is
  unrecoverable; the app says so and offers to create a new one. (Same
  trade-off as the Touch ID flow on macOS.)
- SAF permission revoked → folder shown as unavailable with "Pick again".
- Partial save/upload failure → per-file result list; failed files are
  removed from the destination so no truncated ciphertext remains.

## Testing

- **Go**: `vault.Init` round-trip + real-gocryptfs interop test; `mobile`
  package tests for open/list/read/write/move/delete over both `OpenLocal` and
  a fake `Storage`; existing fixtures in `webserver/vault/testdata` read via
  `mobile`.
- **Kotlin unit** (JVM): `ApiClient` against OkHttp `MockWebServer`;
  `VaultRepository` with fake sources; state file encrypt/decrypt.
- **Instrumented** (emulator): `vaultcore` opens the committed fixture vault
  and the built-in vault round-trips a file; a "no plaintext on disk" test
  walks `filesDir`, `cacheDir`, `noBackupFilesDir` after viewing and saving
  and asserts no file contains known plaintext bytes.
- **CI** `android-ci.yml`: Go tests, `gomobile bind`, `./gradlew test
  assembleDebug`, instrumented tests on an emulator (API 34).

## Release

`release.yml` gains an `android` job that runs in parallel with `webserver`:
set up Go + JDK 21 + Android SDK/NDK, `make sync`, `gomobile bind`,
`./gradlew assembleRelease` with `versionName` = tag version and
`versionCode` derived from it (`major*10000 + minor*100 + patch`), signs with a
keystore from repository secrets, and uploads
`imagethat-android_vX.Y.Z.apk`. The publish job picks it up with the webserver
archives, so it is covered by `SHA256SUMS`.

Secrets needed (one-time setup by the user): `ANDROID_KEYSTORE_BASE64`,
`ANDROID_KEYSTORE_PASSWORD`, `ANDROID_KEY_ALIAS`, `ANDROID_KEY_PASSWORD`. If
they are missing the android job fails rather than publishing an unsigned or
debug-signed APK. A `make android-keystore` helper prints the commands to
create one.

## Out of scope for v1

Play Store distribution, Google Drive vaults, exporting decrypted files,
editing images, multiple simultaneous accounts on one server, tablets-specific
layouts beyond what Compose adapts automatically.

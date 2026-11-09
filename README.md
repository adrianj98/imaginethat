# ImageThat

macOS image/video explorer with gocryptfs encrypted vault support. Decrypted data never touches disk.

## Build & Run

Requires macOS 14+, Go, and Swift. The gocryptfs engine is the Go module in
`core/`, shared with the Android app and the webserver; the Mac app links it
through `gocryptfs-lib`, a thin cgo shim. See
`docs/superpowers/specs/2026-10-29-go-core-design.md` for where this is going.

```
  make build    # compiles Go lib + Swift app
  make run      # builds + launches the app                                                                                                    
  make clean    # wipe all build artifacts
```

### Install

Builds the app and copies it into `/Applications` (Apple silicon only):

```bash
Scripts/install-mac.sh           # or: make install
Scripts/install-mac.sh --user    # ~/Applications, no admin rights
Scripts/install-mac.sh --open    # launch it afterwards
```

#### Stop the keychain prompt after Touch ID

Touch ID unlocks a master key in your login keychain, and macOS checks two
things about the app before handing it over: its signature, and a "partition
list". `make app` signs ad hoc by default, and that signature changes with every
build. A stable self-signed certificate fixes the first check:

```bash
Scripts/make-signing-cert.sh    # asks for your login password once, to trust it
Scripts/install-mac.sh
```

`make app` picks up `ImageThat Local` on its own. (By hand instead: Keychain
Access > Certificate Assistant > Create a Certificate…, name `ImageThat Local`,
Self-Signed Root, Code Signing.)

A self-signed certificate has no Team ID, so the partition list still records
each build's hash, and macOS can ask for your login password after a rebuild.
There are two ways around that:

- Sign with an Apple Development certificate, whose Team ID stays the same
  between builds: `make app SIGN_IDENTITY="Apple Development: ..."`.
- Clear the master key's partition list. It asks for your login password, and
  has to be repeated if the app ever saves a new master key:

```bash
security set-generic-password-partition-list -S "" -s com.adrianm.imagethat -a master-key ~/Library/Keychains/login.keychain-db
```

Run it in Terminal, and don't pass the password with `-k`, which would put it in
your shell history.

The app icons for Mac, Android and the web UI all come from
`Scripts/make-icons.swift`; run `make icons` after changing the artwork.

## Shortcuts

- **Cmd+O** — open folder
- **Cmd+Shift+O** — open encrypted vault
- **Arrow keys** — navigate images
- **Escape** — back to grid
- **Spacebar** — play/pause video

## Android

An Android app (`android/`, Kotlin + Compose, Android 10+) that opens gocryptfs vaults from a device folder and browses vaults served by `imagethat-web`. It embeds the same Go vault engine (`core/mobile`) through a gomobile-built `vaultcore.aar`.

Build needs JDK 21, the Android SDK with NDK 26.1.10909125, Go, and `gomobile`/`gobind` on `PATH`:

```
  make android-debug   # builds the debug APK
  make android-test    # runs the JVM unit tests
```

### Releases

Pushing a `vX.Y.Z` tag builds a signed APK (`imagethat-android_vX.Y.Z.apk`) and attaches it to the GitHub release with the other artifacts. `versionName` is the tag; `versionCode` is `major*10000 + minor*100 + patch`.

Every release must be signed with the same key or installs cannot upgrade in place. The key is `~/imagethat-release.jks` (alias `imagethat`), with its passwords in `~/imagethat-release-keystore.txt`, and is stored as the GitHub secrets `ANDROID_KEYSTORE_BASE64`, `ANDROID_KEYSTORE_PASSWORD`, `ANDROID_KEY_ALIAS` and `ANDROID_KEY_PASSWORD`. Back both files up somewhere safe: if they are lost, the app can only be reinstalled, not updated. Never commit them.

The signing certificate SHA-256 is `71:E3:53:3E:20:10:39:81:E6:1F:42:2C:3B:19:D2:DF:79:28:3B:1F:B5:AC:87:5D:E0:A8:46:14:9D:B2:19:6B`. Locally, `ANDROID_KEYSTORE_FILE`, `ANDROID_KEYSTORE_PASSWORD`, `ANDROID_KEY_ALIAS`, `ANDROID_KEY_PASSWORD` and `VERSION_NAME` in the environment let `./gradlew :app:assembleRelease` sign a build; without them a release build fails.

# aj-log

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

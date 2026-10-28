# aj-log

## 2026-10-28 — kunj blueprint

- Added `.kunj/blueprint.json` so `kunj ct start` can build a snapshot with the webserver's dependencies already installed.
  - Why: branch containers start in seconds from the snapshot instead of re-running `go mod download` / `npm ci` each time.
  - Only covers `webserver/` (Go 1.24 + the Vite/React UI): the macOS app needs Xcode and can't build in kunj's Linux sandbox.
  - `ffmpeg` via apt for server-side video thumbnails; FUSE/gocryptfs left out because the sandbox can't mount, so use `test-short` there.
  - Lockfiles point at `webserver/go.sum` and `webserver/web/package-lock.json` since neither is at the repo root (kunj's default).

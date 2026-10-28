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

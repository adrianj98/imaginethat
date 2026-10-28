import Foundation
import AVFoundation
import Darwin
import CLibGocryptfs
import ImageThatCore

// Large-video playback from a gocryptfs vault on a slow network drive.
//
// A real NAS mount can't be faked portably, so the vault is served to the Go
// library through its VFS callbacks (the same hook Drive vaults use) by a
// SlowDrive that charges latency and bandwidth on every read. Everything above
// that — GocryptfsReadRange, EncryptedResourceLoader, AVFoundation — is the
// app's real code path.
//
// The video is written with ffmpeg's default layout, moov atom at the end, so
// AVFoundation has to reach the tail of the file before it can play anything:
// the worst case for a slow link.
//
// Env knobs:
//   IMAGETHAT_SKIP_SLOW_VIDEO=1        skip this section
//   IMAGETHAT_SLOW_VIDEO_SECONDS=20    clip length (≈28 MB/s of noise, so ~550 MB)
//   IMAGETHAT_SLOW_LATENCY_MS=2        latency charged per read call
//   IMAGETHAT_SLOW_MBPS=10             bandwidth in MB/s

enum SlowVideoTest {
    static let fixtureDir = URL(fileURLWithPath: "/tmp/imagethat-test-slowvideo")
    static let password = "slowpass123"
    static let videoName = "big.mp4"

    /// Bound for each AVFoundation step. Generous, but a hang still fails.
    static let stepTimeout: TimeInterval = 90

    static func run(check: (String, Bool, String) -> Void) async {
        print("\n--- Slow Drive Large Video Tests ---")

        let env = ProcessInfo.processInfo.environment
        if env["IMAGETHAT_SKIP_SLOW_VIDEO"] == "1" {
            print("  SKIP: IMAGETHAT_SKIP_SLOW_VIDEO=1")
            return
        }
        guard let ffmpeg = which("ffmpeg"), let gocryptfs = which("gocryptfs") else {
            print("  SKIP: needs ffmpeg and gocryptfs on PATH")
            return
        }

        let seconds = Int(env["IMAGETHAT_SLOW_VIDEO_SECONDS"] ?? "") ?? 20
        let latency = (Double(env["IMAGETHAT_SLOW_LATENCY_MS"] ?? "") ?? 2) / 1000
        let bytesPerSecond = (Double(env["IMAGETHAT_SLOW_MBPS"] ?? "") ?? 10) * 1_048_576

        let plainURL: URL
        do {
            plainURL = try prepareFixture(seconds: seconds, ffmpeg: ffmpeg, gocryptfs: gocryptfs)
        } catch {
            check("build large video fixture", false, "\(error)")
            return
        }

        let drive = SlowDrive(root: fixtureDir.appendingPathComponent("vault"),
                              latency: latency, bytesPerSecond: bytesPerSecond)
        let reader: SlowVaultReader
        do {
            reader = try SlowVaultReader(drive: drive, password: password)
        } catch {
            check("open vault over slow drive", false, "\(error)")
            return
        }
        check("open vault over slow drive", true, "")

        let plainSize = (try? FileManager.default.attributesOfItem(atPath: plainURL.path)[.size] as? Int64) ?? 0
        let size = (try? reader.fileSize(path: videoName)) ?? -1
        check("fileSize over slow drive matches plaintext", size == plainSize && size > 0,
              "got \(size), want \(plainSize)")
        print("  info: \(plainSize / 1_048_576) MB video, \(Int(latency * 1000)) ms/read, \(Int(bytesPerSecond / 1_048_576)) MB/s")

        // Byte-exact reads at the places AVFoundation goes: the head, the moov
        // at the tail, block-straddling offsets and the final short block.
        do {
            let plain = try FileHandle(forReadingFrom: plainURL)
            defer { try? plain.close() }
            let block: Int64 = 4096
            let offsets: [Int64] = [0, block - 7, plainSize / 2 + 123, plainSize - 300_000, plainSize - 10]
            var mismatches: [Int64] = []
            for off in offsets {
                let len = Int32(min(Int64(256 * 1024), plainSize - off))
                let got = try reader.readRange(path: videoName, offset: off, length: len)
                try plain.seek(toOffset: UInt64(off))
                let want = plain.readData(ofLength: Int(len))
                if got != want { mismatches.append(off) }
            }
            check("readRange over slow drive is byte-exact", mismatches.isEmpty, "mismatch at \(mismatches)")
        } catch {
            check("readRange over slow drive is byte-exact", false, "\(error)")
        }

        // Metadata: the moov is at the end of a large file.
        drive.resetCounters()
        let loader = EncryptedResourceLoader(bridge: reader, relativePath: videoName)
        let asset = makeAsset(loader: loader, label: "slowvideo.meta")
        let start = Date()
        let duration: CMTime? = try? await withTimeout(stepTimeout) { try await asset.load(.duration) }
        let dSeconds = duration.map(CMTimeGetSeconds) ?? 0
        check("loads duration of large video (moov at end)", abs(dSeconds - Double(seconds)) < 1,
              "got \(dSeconds)s after \(elapsed(start))")
        let tracks = (try? await withTimeout(stepTimeout) { try await asset.loadTracks(withMediaType: .video) }) ?? []
        check("finds the video track", tracks.count == 1, "got \(tracks.count)")
        print("  info: metadata in \(elapsed(start)), \(drive.bytesRead / 1024) KB pulled from drive")

        // Frames at the start and near the end — the app's thumbnail path, and
        // a seek that only works if Range reads land deep in the file.
        for at in [1.0, Double(seconds) - 2] {
            let t0 = Date()
            let ok = await frame(at: at, loader: EncryptedResourceLoader(bridge: reader, relativePath: videoName))
            check("extracts frame at \(Int(at))s", ok, "after \(elapsed(t0))")
        }

        // Reading a large file must not mean downloading it.
        let pulled = drive.bytesRead
        check("metadata + 2 frames read under a third of the file",
              pulled < plainSize / 3,
              "pulled \(pulled / 1_048_576) of \(plainSize / 1_048_576) MB")

        await playback(reader: reader, drive: drive, seconds: seconds, check: check)

        // Closing the video must stop the reads; on a metered or shared link a
        // leaked to-EOF read would keep pulling the rest of the file. By now
        // the player, item and loader are released, as VideoPlayerView does
        // in onDisappear.
        try? await Task.sleep(for: .seconds(2))
        let settled = drive.bytesRead
        try? await Task.sleep(for: .seconds(3))
        let leaked = drive.bytesRead - settled
        check("reads stop after player is closed", leaked < 1_048_576,
              "\(leaked / 1024) KB read after close")
    }

    // MARK: - Playback

    @MainActor
    static func playback(reader: SlowVaultReader, drive: SlowDrive, seconds: Int,
                         check: (String, Bool, String) -> Void) async {
        let loader = EncryptedResourceLoader(bridge: reader, relativePath: videoName)
        let item = AVPlayerItem(asset: makeAsset(loader: loader, label: "slowvideo.play"))
        let player = AVPlayer(playerItem: item)
        player.isMuted = true

        let ready = await waitUntil(stepTimeout) { item.status != .unknown }
        check("player item becomes ready over slow drive", ready && item.status == .readyToPlay,
              "status=\(item.status.rawValue) error=\(String(describing: item.error))")
        guard item.status == .readyToPlay else { return }

        player.play()
        let advanced = await waitUntil(stepTimeout) { CMTimeGetSeconds(player.currentTime()) > 0.5 }
        check("playback advances from start", advanced, "t=\(CMTimeGetSeconds(player.currentTime()))")

        // Jump deep into the file, as a user scrubbing would.
        let target = Double(seconds) * 0.75
        let seeked = await withCheckedContinuation { (c: CheckedContinuation<Bool, Never>) in
            player.seek(to: CMTime(seconds: target, preferredTimescale: 600),
                        toleranceBefore: .zero, toleranceAfter: .zero) { c.resume(returning: $0) }
        }
        check("seek to 75% completes", seeked, "")
        player.play()
        let advancedAfterSeek = await waitUntil(stepTimeout) {
            CMTimeGetSeconds(player.currentTime()) > target + 0.5
        }
        check("playback advances after seek", advancedAfterSeek,
              "t=\(CMTimeGetSeconds(player.currentTime())) want > \(target + 0.5)")

        player.pause()
    }

    // MARK: - Helpers

    static func makeAsset(loader: EncryptedResourceLoader, label: String) -> AVURLAsset {
        // Same URL shape as VaultSource.createPlayerItem.
        var components = URLComponents()
        components.scheme = "imagethat-encrypted"
        components.host = "vault"
        components.path = "/\(videoName)"
        let asset = AVURLAsset(url: components.url!)
        asset.resourceLoader.setDelegate(loader, queue: DispatchQueue(label: label))
        return asset
    }

    static func frame(at seconds: Double, loader: EncryptedResourceLoader) async -> Bool {
        let asset = makeAsset(loader: loader, label: "slowvideo.frame")
        let generator = AVAssetImageGenerator(asset: asset)
        generator.maximumSize = CGSize(width: 320, height: 320)
        let time = CMTime(seconds: seconds, preferredTimescale: 600)
        let image = try? await withTimeout(stepTimeout) { try await generator.image(at: time).image }
        return image.map { $0.width > 0 } ?? false
    }

    /// Generates the video and encrypts it into a fresh vault once; later runs
    /// reuse it while the parameters match.
    static func prepareFixture(seconds: Int, ffmpeg: String, gocryptfs: String) throws -> URL {
        let fm = FileManager.default
        let plain = fixtureDir.appendingPathComponent("plain/\(videoName)")
        let vault = fixtureDir.appendingPathComponent("vault")
        let marker = fixtureDir.appendingPathComponent("fixture-\(seconds)s.ok")
        if fm.fileExists(atPath: marker.path) { return plain }

        print("  info: building \(seconds)s fixture in \(fixtureDir.path) ...")
        try? fm.removeItem(at: fixtureDir)
        try fm.createDirectory(at: plain.deletingLastPathComponent(), withIntermediateDirectories: true)
        try fm.createDirectory(at: vault, withIntermediateDirectories: true)

        // Per-frame noise defeats compression so the file is genuinely large;
        // one keyframe a second keeps seeks honest. No +faststart: moov stays
        // at the end.
        try exec(ffmpeg, ["-hide_banner", "-loglevel", "error", "-y",
                          "-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=30:duration=\(seconds)",
                          "-vf", "noise=alls=60:allf=t",
                          "-c:v", "libx264", "-preset", "ultrafast", "-crf", "10", "-g", "30",
                          "-pix_fmt", "yuv420p", plain.path])
        try exec(gocryptfs, ["-init", "-q", "-scryptn", "10", "-extpass", "echo \(password)", vault.path])

        let bridge = try GocryptfsBridge(vaultPath: vault, password: password)
        try bridge.writeFile(path: videoName, data: try Data(contentsOf: plain))

        fm.createFile(atPath: marker.path, contents: nil)
        return plain
    }

    static func exec(_ tool: String, _ args: [String]) throws {
        let p = Process()
        p.executableURL = URL(fileURLWithPath: tool)
        p.arguments = args
        try p.run()
        p.waitUntilExit()
        guard p.terminationStatus == 0 else {
            throw NSError(domain: "SlowVideoTest", code: Int(p.terminationStatus),
                          userInfo: [NSLocalizedDescriptionKey: "\(tool) exited \(p.terminationStatus)"])
        }
    }

    static func which(_ tool: String) -> String? {
        let dirs = (ProcessInfo.processInfo.environment["PATH"] ?? "").split(separator: ":").map(String.init)
            + ["/opt/homebrew/bin", "/usr/local/bin"]
        return dirs.map { "\($0)/\(tool)" }.first { FileManager.default.isExecutableFile(atPath: $0) }
    }

    static func elapsed(_ since: Date) -> String {
        String(format: "%.1fs", Date().timeIntervalSince(since))
    }

    @MainActor
    static func waitUntil(_ timeout: TimeInterval, _ condition: () -> Bool) async -> Bool {
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            if condition() { return true }
            try? await Task.sleep(for: .milliseconds(100))
        }
        return condition()
    }

    struct TimedOut: Error {}

    static func withTimeout<T: Sendable>(_ seconds: TimeInterval,
                                         _ body: @escaping @Sendable () async throws -> T) async throws -> T {
        try await withThrowingTaskGroup(of: T.self) { group in
            group.addTask { try await body() }
            group.addTask {
                try await Task.sleep(for: .seconds(seconds))
                throw TimedOut()
            }
            let first = try await group.next()!
            group.cancelAll()
            return first
        }
    }
}

// MARK: - Simulated network drive

private let slowReadAtFn: VFSReadAtFn = { ctx, path, offset, length, buf, bufSize in
    guard let ctx, let path, let buf else { return -1 }
    let drive = Unmanaged<SlowDrive>.fromOpaque(ctx).takeUnretainedValue()
    return drive.readAt(String(cString: path), offset: offset, length: min(length, bufSize), into: buf)
}

private let slowFileSizeFn: VFSFileSizeFn = { ctx, path in
    guard let ctx, let path else { return -1 }
    let drive = Unmanaged<SlowDrive>.fromOpaque(ctx).takeUnretainedValue()
    return drive.size(of: String(cString: path)) ?? -1
}

private let slowListDirFn: VFSListDirFn = { ctx, path in
    guard let ctx, let path else { return ListResult(entries: nil, count: 0, error: nil) }
    let drive = Unmanaged<SlowDrive>.fromOpaque(ctx).takeUnretainedValue()
    return drive.listDir(String(cString: path))
}

/// Serves a local cipher directory as if it were across a slow link: every
/// read sleeps for a round trip plus the transfer time at the given bandwidth.
final class SlowDrive: @unchecked Sendable {
    let root: URL
    let latency: TimeInterval
    let bytesPerSecond: Double

    private let lock = NSLock()
    private var _bytesRead: Int64 = 0

    init(root: URL, latency: TimeInterval, bytesPerSecond: Double) {
        self.root = root
        self.latency = latency
        self.bytesPerSecond = bytesPerSecond
    }

    var bytesRead: Int64 {
        lock.lock(); defer { lock.unlock() }
        return _bytesRead
    }

    func resetCounters() {
        lock.lock(); _bytesRead = 0; lock.unlock()
    }

    func url(_ path: String) -> URL {
        path.isEmpty ? root : root.appendingPathComponent(path)
    }

    func readAt(_ path: String, offset: Int64, length: Int64, into buf: UnsafeMutablePointer<UInt8>) -> Int64 {
        let fd = open(url(path).path, O_RDONLY)
        guard fd >= 0 else { return -1 }
        defer { close(fd) }
        let n = pread(fd, buf, Int(length), off_t(offset))
        guard n >= 0 else { return -1 }
        Thread.sleep(forTimeInterval: latency + Double(n) / bytesPerSecond)
        lock.lock(); _bytesRead += Int64(n); lock.unlock()
        return Int64(n)
    }

    func size(of path: String) -> Int64? {
        (try? FileManager.default.attributesOfItem(atPath: url(path).path)[.size] as? Int64) ?? nil
    }

    func listDir(_ path: String) -> ListResult {
        Thread.sleep(forTimeInterval: latency)
        let dir = url(path)
        guard let names = try? FileManager.default.contentsOfDirectory(atPath: dir.path), !names.isEmpty else {
            return ListResult(entries: nil, count: 0, error: nil)
        }
        let entries = malloc(MemoryLayout<DirEntry>.stride * names.count)!.bindMemory(to: DirEntry.self, capacity: names.count)
        for (i, name) in names.enumerated() {
            var isDir: ObjCBool = false
            FileManager.default.fileExists(atPath: dir.appendingPathComponent(name).path, isDirectory: &isDir)
            let childPath = path.isEmpty ? name : "\(path)/\(name)"
            entries[i] = DirEntry(name: strdup(name), is_dir: isDir.boolValue ? 1 : 0,
                                  size: isDir.boolValue ? 0 : (size(of: childPath) ?? 0))
        }
        return ListResult(entries: entries, count: Int32(names.count), error: nil)
    }
}

/// A gocryptfs session whose ciphertext comes from a SlowDrive.
final class SlowVaultReader: EncryptedRangeReading, @unchecked Sendable {
    private let sessionID: Int32
    // Retained for the session's lifetime: the C callbacks hold it unretained.
    let drive: SlowDrive

    init(drive: SlowDrive, password: String) throws {
        self.drive = drive
        let config = try Data(contentsOf: drive.url("gocryptfs.conf"))
        let ctx = Unmanaged.passUnretained(drive).toOpaque()
        let id: Int32 = config.withUnsafeBytes { configPtr in
            password.withCString { passPtr in
                GocryptfsOpenVFS(
                    UnsafeMutablePointer(mutating: configPtr.bindMemory(to: Int8.self).baseAddress!),
                    Int32(config.count),
                    UnsafeMutablePointer(mutating: passPtr),
                    slowReadAtFn, slowFileSizeFn, slowListDirFn, ctx)
            }
        }
        guard id > 0 else { throw GocryptfsError.cryptoError }
        sessionID = id
    }

    deinit { GocryptfsClose(sessionID) }

    func readRange(path: String, offset: Int64, length: Int32) throws -> Data {
        var buffer = Data(count: Int(length))
        let n: Int32 = buffer.withUnsafeMutableBytes { bufPtr in
            path.withCString { pathPtr in
                GocryptfsReadRange(sessionID, UnsafeMutablePointer(mutating: pathPtr), offset, length, bufPtr.baseAddress!)
            }
        }
        guard n >= 0 else { throw GocryptfsError.decryptFailed("readRange failed at offset=\(offset)") }
        return buffer.prefix(Int(n))
    }

    func fileSize(path: String) throws -> Int64 {
        let size = path.withCString { GocryptfsFileSize(sessionID, UnsafeMutablePointer(mutating: $0)) }
        guard size >= 0 else { throw GocryptfsError.decryptFailed("fileSize failed") }
        return size
    }
}

import Foundation
import AppKit
import AVFoundation
import ImageThatCore

// RemoteVaultSource against a real imagethat-web server (webserver/ in this
// repo): browse, stream, and every write the app makes, end to end.
//
// Needs webserver/bin/imagethat-web (make web-build), gocryptfs and ffmpeg.
// IMAGETHAT_SKIP_REMOTE=1 skips the section.

enum RemoteServerTest {
    static let fixtureDir = URL(fileURLWithPath: "/tmp/imagethat-test-server")
    static let videoFixture = URL(fileURLWithPath: "/tmp/imagethat-test-remote-video.mp4")
    static let password = "remotepass123"
    /// Short, so the test can watch the server expire the session.
    static let idleTimeout = 3

    static func run(check: (String, Bool, String) -> Void) async {
        print("\n--- Remote Server (imagethat-web) Tests ---")

        let env = ProcessInfo.processInfo.environment
        if env["IMAGETHAT_SKIP_REMOTE"] == "1" {
            print("  SKIP: IMAGETHAT_SKIP_REMOTE=1")
            return
        }
        let serverBin = URL(fileURLWithPath: FileManager.default.currentDirectoryPath)
            .appendingPathComponent("webserver/bin/imagethat-web").path
        guard FileManager.default.isExecutableFile(atPath: serverBin),
              let gocryptfs = SlowVideoTest.which("gocryptfs"),
              let ffmpeg = SlowVideoTest.which("ffmpeg") else {
            print("  SKIP: needs webserver/bin/imagethat-web (make web-build), gocryptfs and ffmpeg")
            return
        }

        let server: Process
        let baseURL: URL
        do {
            try? FileManager.default.removeItem(at: fixtureDir)
            try FileManager.default.createDirectory(at: fixtureDir.appendingPathComponent("rw"),
                                                    withIntermediateDirectories: true)
            try SlowVideoTest.exec(gocryptfs, ["-init", "-q", "-scryptn", "10", "-extpass", "echo \(password)",
                                               fixtureDir.appendingPathComponent("rw").path])
            if !FileManager.default.fileExists(atPath: videoFixture.path) {
                try SlowVideoTest.exec(ffmpeg, ["-hide_banner", "-loglevel", "error", "-y",
                                                "-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=30:duration=6",
                                                "-vf", "noise=alls=60:allf=t",
                                                "-c:v", "libx264", "-preset", "ultrafast", "-crf", "10", "-g", "30",
                                                "-pix_fmt", "yuv420p", videoFixture.path])
            }
            (server, baseURL) = try startServer(serverBin)
        } catch {
            check("start imagethat-web", false, "\(error)")
            return
        }
        defer { server.terminate(); server.waitUntilExit() }
        check("start imagethat-web", true, "")

        let client = RemoteServerClient(baseURL: baseURL)

        let normalized = [
            AppState.normalizeServerURL("nas.local:8347"),
            AppState.normalizeServerURL("localhost:8347/"),
            AppState.normalizeServerURL(" http://10.0.0.5:8347 "),
            AppState.normalizeServerURL("ftp://x"),
        ]
        check("server address normalization",
              normalized == ["https://nas.local:8347", "http://localhost:8347", "http://10.0.0.5:8347", nil],
              "got \(normalized)")

        // Vault discovery and unlock.
        guard let vault = try? await client.listVaults().first else {
            check("lists the vault", false, "no vaults")
            return
        }
        check("lists the vault, locked", vault.name == "rw" && !vault.unlocked, "got \(vault)")

        do {
            try await client.unlock(vaultID: vault.id, password: "nope")
            check("wrong password rejected", false, "unlock succeeded")
        } catch RemoteServerError.wrongPassword {
            check("wrong password rejected", true, "")
        } catch {
            check("wrong password rejected", false, "\(error)")
        }

        do {
            try await client.unlock(vaultID: vault.id, password: password)
            check("unlock with password", true, "")
        } catch {
            check("unlock with password", false, "\(error)")
            return
        }

        let root = RemoteVaultSource(client: client, vaultID: vault.id, vaultName: vault.name)

        do {
            try await root.createDirectory(named: "Photos")
            let dirs = try await root.listSubdirectories()
            check("createDirectory", dirs.map(\.name) == ["Photos"], "got \(dirs.map(\.name))")

            // Images: upload (streamed), list, load, thumbnail. The name has a
            // '+' and a space to catch query-string encoding slips.
            let photos = root.source(at: "Photos") as! RemoteVaultSource
            let png = makePNG(width: 640, height: 480, red: true)
            let name = "a+b c.png"
            try await photos.writeChunks(chunked(png), to: "Photos/\(name)", totalSize: Int64(png.count))
            let items = try await photos.listMedia()
            check("upload + listMedia", items.map(\.name) == [name], "got \(items.map(\.name))")
            guard let image = items.first else { return }

            let loaded = try await photos.loadImage(for: image)
            check("loadImage", loaded.size.width == 640, "size=\(loaded.size)")
            let thumb = try await photos.loadThumbnail(for: image, size: CGSize(width: 100, height: 100))
            check("loadThumbnail (server JPEG)", thumb.size.width > 0 && thumb.size.width <= 200, "size=\(thumb.size)")
            let size = try await photos.fileSize(for: image)
            check("fileSize", size == Int64(png.count), "got \(size), want \(png.count)")

            // Editing: copy and replace.
            let edited = makePNG(width: 320, height: 240, red: false)
            let copy = try await photos.saveImageCopy(edited, for: image, suffix: "-edited")
            let afterCopy = try await photos.listMedia().map(\.name)
            check("saveImageCopy", copy.name == "a+b c-edited.png" && afterCopy.contains(copy.name),
                  "got \(copy.name), listing \(afterCopy)")
            try await photos.saveImage(edited, for: image)
            let replaced = try await photos.loadImage(for: image)
            let afterSave = try await photos.listMedia().map(\.name)
            check("saveImage replaces in place", replaced.size.width == 320 && afterSave.count == 2,
                  "size=\(replaced.size) items=\(afterSave)")

            // Move into the vault root, then delete.
            try await photos.moveItem(copy, to: "")
            let rootNames = try await root.listMedia().map(\.name)
            check("moveItem", rootNames == [copy.name], "root has \(rootNames)")
            let moved = try await root.listMedia()[0]
            try await root.deleteItem(moved)
            check("deleteItem", try await root.listMedia().isEmpty, "")

            // Video: streamed upload of a large file, byte-exact range reads,
            // playback with a seek, thumbnail.
            let videoSize = ((try FileManager.default.attributesOfItem(atPath: videoFixture.path)[.size]) as? Int64) ?? 0
            let local = FolderSource(rootURL: videoFixture.deletingLastPathComponent())
            let t0 = Date()
            try await root.writeChunks(local.readChunks(for: MediaItem(url: videoFixture), chunkSize: 1_048_576),
                                       to: "clip.mp4", totalSize: videoSize)
            guard let video = try await root.listMedia().first(where: { $0.name == "clip.mp4" }) else {
                check("upload large video", false, "clip.mp4 missing")
                return
            }
            let remoteSize = try await root.fileSize(for: video)
            check("upload large video", remoteSize == videoSize,
                  "\(remoteSize) vs \(videoSize) bytes, \(SlowVideoTest.elapsed(t0))")
            print("  info: \(videoSize / 1_048_576) MB uploaded in \(SlowVideoTest.elapsed(t0))")

            let reader = RemoteRangeReader(client: client, vaultID: vault.id)
            let fh = try FileHandle(forReadingFrom: videoFixture)
            defer { try? fh.close() }
            var mismatches: [Int64] = []
            for off in [0, 4089, videoSize / 2, videoSize - 100_000, videoSize - 10] {
                let len = Int32(min(262_144, videoSize - off))
                let got = try reader.readRange(path: "clip.mp4", offset: off, length: len)
                try fh.seek(toOffset: UInt64(off))
                if got != fh.readData(ofLength: Int(len)) { mismatches.append(off) }
            }
            let pastEnd = try reader.readRange(path: "clip.mp4", offset: videoSize + 10, length: 100)
            check("range reads byte-exact, empty past EOF", mismatches.isEmpty && pastEnd.isEmpty,
                  "mismatch at \(mismatches), pastEnd=\(pastEnd.count)")

            await playback(try root.createPlayerItem(for: video), check: check)

            let vthumb = try? await root.loadThumbnail(for: video, size: CGSize(width: 160, height: 160))
            check("video thumbnail", (vthumb?.size.width ?? 0) > 0, "")

            // The server locks idle sessions; the client must re-unlock.
            try await Task.sleep(for: .seconds(idleTimeout + 2))
            let afterIdle = try await root.listMedia().map(\.name)
            check("re-unlocks after server idle timeout", afterIdle.contains("clip.mp4"), "got \(afterIdle)")

            try await photos.deleteCurrentDirectory()
            check("deleteCurrentDirectory", try await root.listSubdirectories().isEmpty, "")

            try await client.lock(vaultID: vault.id)
            do {
                _ = try await root.listMedia()
                check("lock forgets the vault", false, "list still works")
            } catch RemoteServerError.http(let status, _) {
                check("lock forgets the vault", status == 401, "status \(status)")
            }
        } catch {
            check("remote vault operations", false, "\(error)")
        }

        await runClientDecrypt(serverBin, check: check)
    }

    /// The same vault served in client-decrypt mode: the client logs in with
    /// the server's access password and decrypts here, through the Go library.
    static func runClientDecrypt(_ serverBin: String, check: (String, Bool, String) -> Void) async {
        print("  -- client-decrypt mode --")
        let accessPassword = "server-access-123"
        let server: Process
        let baseURL: URL
        do {
            let hashFile = fixtureDir.appendingPathComponent("access-hash")
            try hashPassword(serverBin, accessPassword).write(to: hashFile)
            (server, baseURL) = try startServer(serverBin, extra: [
                "-mode", "client-decrypt", "-access-password-file", hashFile.path,
            ])
        } catch {
            check("start client-decrypt server", false, "\(error)")
            return
        }
        defer { server.terminate(); server.waitUntilExit() }

        let client = RemoteServerClient(baseURL: baseURL)
        do {
            let mode = try await client.serverMode()
            check("reports client-decrypt mode", mode == .clientDecrypt, "got \(mode)")

            do {
                _ = try await client.listVaults()
                check("vaults need the access password", false, "listed without login")
            } catch RemoteServerError.accessPasswordRequired {
                check("vaults need the access password", true, "")
            }
            do {
                try await client.login(accessPassword: "nope")
                check("wrong access password rejected", false, "login succeeded")
            } catch RemoteServerError.wrongAccessPassword {
                check("wrong access password rejected", true, "")
            }
            try await client.login(accessPassword: accessPassword)
            guard let vault = try await client.listVaults().first else {
                check("lists the vault after login", false, "no vaults")
                return
            }
            check("lists the vault after login", vault.name == "rw" && !vault.unlocked, "got \(vault)")

            do {
                try await client.unlock(vaultID: vault.id, password: "nope")
                check("wrong vault password rejected locally", false, "unlock succeeded")
            } catch RemoteServerError.wrongPassword {
                check("wrong vault password rejected locally", true, "")
            }
            try await client.unlock(vaultID: vault.id, password: password)
            let unlocked = try await client.listVaults().first?.unlocked ?? false
            check("unlock opens the vault on this Mac", unlocked, "")

            let root = RemoteVaultSource(client: client, vaultID: vault.id, vaultName: vault.name)
            guard let video = try await root.listMedia().first(where: { $0.name == "clip.mp4" }) else {
                check("lists decrypted names", false, "clip.mp4 missing")
                return
            }
            let videoSize = ((try FileManager.default.attributesOfItem(atPath: videoFixture.path)[.size]) as? Int64) ?? 0
            let size = try await root.fileSize(for: video)
            check("lists decrypted names and sizes", size == videoSize, "\(size) vs \(videoSize)")

            let reader = RemoteRangeReader(client: client, vaultID: vault.id)
            let fh = try FileHandle(forReadingFrom: videoFixture)
            defer { try? fh.close() }
            var mismatches: [Int64] = []
            for off in [0, 4089, videoSize / 2, videoSize - 10] {
                let len = Int32(min(262_144, videoSize - off))
                let got = try reader.readRange(path: "clip.mp4", offset: off, length: len)
                try fh.seek(toOffset: UInt64(off))
                if got != fh.readData(ofLength: Int(len)) { mismatches.append(off) }
            }
            check("range reads decrypted here, byte-exact", mismatches.isEmpty, "mismatch at \(mismatches)")

            await playback(try root.createPlayerItem(for: video), check: check)
            let vthumb = try? await root.loadThumbnail(for: video, size: CGSize(width: 160, height: 160))
            check("video thumbnail rendered here", (vthumb?.size.width ?? 0) > 0, "")

            do {
                try await root.createDirectory(named: "Nope")
                check("writes refused", false, "mkdir succeeded")
            } catch RemoteServerError.readOnly {
                check("writes refused", true, "")
            }

            try await client.lock(vaultID: vault.id)
            do {
                _ = try await root.listMedia()
                check("lock drops the local vault", false, "list still works")
            } catch RemoteServerError.vaultLocked {
                check("lock drops the local vault", true, "")
            }
        } catch {
            check("client-decrypt operations", false, "\(error)")
        }
    }

    /// Runs `imagethat-web -hash-password`, feeding the password on stdin.
    static func hashPassword(_ bin: String, _ password: String) throws -> Data {
        let p = Process()
        p.executableURL = URL(fileURLWithPath: bin)
        p.arguments = ["-hash-password"]
        let input = Pipe(), output = Pipe()
        p.standardInput = input
        p.standardOutput = output
        p.standardError = FileHandle.nullDevice
        try p.run()
        input.fileHandleForWriting.write(Data((password + "\n").utf8))
        try input.fileHandleForWriting.close()
        let data = output.fileHandleForReading.readDataToEndOfFile()
        p.waitUntilExit()
        guard p.terminationStatus == 0, !data.isEmpty else {
            throw NSError(domain: "RemoteServerTest", code: 3, userInfo: [NSLocalizedDescriptionKey: "hash-password failed"])
        }
        return data
    }

    @MainActor
    static func playback(_ item: AVPlayerItem, check: (String, Bool, String) -> Void) async {
        let player = AVPlayer(playerItem: item)
        player.isMuted = true
        let ready = await SlowVideoTest.waitUntil(30) { item.status != .unknown }
        check("remote video ready to play", ready && item.status == .readyToPlay,
              "status=\(item.status.rawValue) error=\(String(describing: item.error))")
        guard item.status == .readyToPlay else { return }
        player.play()
        let started = await SlowVideoTest.waitUntil(30) { CMTimeGetSeconds(player.currentTime()) > 0.5 }
        let target = CMTimeGetSeconds(item.duration) * 0.75
        _ = await player.seek(to: CMTime(seconds: target, preferredTimescale: 600),
                              toleranceBefore: .zero, toleranceAfter: .zero)
        player.play()
        let resumed = await SlowVideoTest.waitUntil(30) { CMTimeGetSeconds(player.currentTime()) > target + 0.5 }
        check("remote video plays and seeks", started && resumed, "t=\(CMTimeGetSeconds(player.currentTime()))")
        player.pause()
        player.replaceCurrentItem(with: nil)
    }

    static func startServer(_ bin: String, extra: [String] = []) throws -> (Process, URL) {
        let port = try freePort()
        let p = Process()
        p.executableURL = URL(fileURLWithPath: bin)
        p.arguments = ["-root", fixtureDir.path, "-addr", "127.0.0.1:\(port)", "-idle-timeout", "\(idleTimeout)s"] + extra
        p.standardOutput = FileHandle.nullDevice
        p.standardError = FileHandle.nullDevice
        try p.run()
        let base = URL(string: "http://127.0.0.1:\(port)")!
        // Wait for it to answer.
        for _ in 0..<100 {
            if (try? Data(contentsOf: base.appendingPathComponent("api/info"))) != nil { return (p, base) }
            Thread.sleep(forTimeInterval: 0.1)
        }
        p.terminate()
        throw NSError(domain: "RemoteServerTest", code: 1, userInfo: [NSLocalizedDescriptionKey: "server did not start"])
    }

    static func freePort() throws -> Int {
        let fd = socket(AF_INET, SOCK_STREAM, 0)
        defer { close(fd) }
        var addr = sockaddr_in()
        addr.sin_family = sa_family_t(AF_INET)
        addr.sin_addr.s_addr = inet_addr("127.0.0.1")
        addr.sin_port = 0
        var len = socklen_t(MemoryLayout<sockaddr_in>.size)
        let ok = withUnsafeMutablePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                bind(fd, $0, len) == 0 && getsockname(fd, $0, &len) == 0
            }
        }
        guard ok else { throw NSError(domain: "RemoteServerTest", code: 2) }
        return Int(UInt16(bigEndian: addr.sin_port))
    }

    static func makePNG(width: Int, height: Int, red: Bool) -> Data {
        let rep = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: width, pixelsHigh: height,
                                   bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
                                   colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)!
        NSGraphicsContext.saveGraphicsState()
        NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: rep)
        (red ? NSColor.red : NSColor.blue).setFill()
        NSRect(x: 0, y: 0, width: width, height: height).fill()
        NSGraphicsContext.restoreGraphicsState()
        return rep.representation(using: .png, properties: [:])!
    }

    static func chunked(_ data: Data, size: Int = 64 * 1024) -> AsyncThrowingStream<Data, Error> {
        AsyncThrowingStream { c in
            var off = 0
            while off < data.count {
                c.yield(data.subdata(in: off..<min(off + size, data.count)))
                off += size
            }
            c.finish()
        }
    }
}

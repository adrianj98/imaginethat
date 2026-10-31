import Foundation
import CLibGocryptfs

/// One fault-tolerant copy or cache fetch in the Go engine (the GocryptfsJob*
/// exports). Reads that fail transiently are retried with backoff and resume
/// where they stopped; the result appears only once complete.
public final class GocryptfsJob: @unchecked Sendable {
    private let id: Int32

    public init() {
        id = GocryptfsJobNew()
    }

    deinit {
        GocryptfsJobFree(id)
    }

    public func cancel() {
        GocryptfsJobCancel(id)
    }

    /// Bytes done and total for the current file, and retries so far.
    public var progress: (done: Int64, total: Int64, retries: Int64) {
        var done: Int64 = 0, total: Int64 = 0, retries: Int64 = 0
        _ = GocryptfsJobProgress(id, &done, &total, &retries)
        return (done, total, retries)
    }

    /// Runs a blocking engine call off the cooperative pool, reporting
    /// progress every 200 ms. Cancelling the Swift task cancels the job.
    fileprivate func run(_ call: @escaping @Sendable (Int32) -> Int32,
                         onProgress: (@Sendable (Int64, Int64) -> Void)?) async throws {
        let poller = Task { [weak self] in
            while !Task.isCancelled, let self {
                let p = self.progress
                onProgress?(p.done, p.total)
                try? await Task.sleep(for: .milliseconds(200))
            }
        }
        defer { poller.cancel() }
        let jobID = id
        let rc = await withTaskCancellationHandler {
            await withCheckedContinuation { (cont: CheckedContinuation<Int32, Never>) in
                DispatchQueue.global(qos: .userInitiated).async { cont.resume(returning: call(jobID)) }
            }
        } onCancel: {
            self.cancel()
        }
        let p = progress
        onProgress?(p.done, p.total)
        switch rc {
        case 0: return
        case -7: throw CancellationError()
        default: throw GocryptfsError.writeFailed(GocryptfsBridge.describeWriteCode(rc))
        }
    }
}

/// Fault-tolerant copies into a vault and the remote file cache.
public enum GocryptfsTransfer {
    /// Copy a plaintext file on disk into a vault at `destPath`.
    public static func copyFile(_ file: URL, into dest: GocryptfsBridge, at destPath: String,
                                onProgress: (@Sendable (Int64, Int64) -> Void)? = nil) async throws {
        let destID = dest.sessionID, src = file.path
        try await GocryptfsJob().run({ job in
            destPath.withCString { d in
                src.withCString { s in
                    GocryptfsCopyFileIn(job, destID, UnsafeMutablePointer(mutating: d), UnsafeMutablePointer(mutating: s))
                }
            }
        }, onProgress: onProgress)
    }

    /// Copy a file from one open vault session (local or remote) into another.
    public static func copy(from source: GocryptfsBridge, path srcPath: String,
                            into dest: GocryptfsBridge, at destPath: String,
                            onProgress: (@Sendable (Int64, Int64) -> Void)? = nil) async throws {
        let destID = dest.sessionID, srcID = source.sessionID
        try await GocryptfsJob().run({ job in
            destPath.withCString { d in
                srcPath.withCString { s in
                    GocryptfsCopyVaultIn(job, destID, UnsafeMutablePointer(mutating: d),
                                         srcID, UnsafeMutablePointer(mutating: s))
                }
            }
        }, onProgress: onProgress)
    }

    /// Keep a file available locally. A remote session opened with a cache
    /// stores its ciphertext on disk; a Drive session pins the file's
    /// ciphertext in the Drive cache, where eviction leaves it alone.
    public static func cacheFile(session: Int32, path: String,
                                 onProgress: (@Sendable (Int64, Int64) -> Void)? = nil) async throws {
        try await GocryptfsJob().run({ job in
            path.withCString { p in GocryptfsCacheFile(job, session, UnsafeMutablePointer(mutating: p)) }
        }, onProgress: onProgress)
    }

    /// Whether the network cache holds the whole file.
    public static func isCached(session: Int32, path: String) -> Bool {
        path.withCString { p in GocryptfsIsCached(session, UnsafeMutablePointer(mutating: p)) == 1 }
    }

    public static func uncache(session: Int32, path: String) {
        _ = path.withCString { p in GocryptfsUncache(session, UnsafeMutablePointer(mutating: p)) }
    }
}

/// The engine's network cache: the ciphertext every network transport (Google
/// Drive, imagethat-web servers, vaults on SMB/NFS mounts) has read, in one
/// directory with one size limit. Files pinned with
/// `GocryptfsTransfer.cacheFile` are never evicted.
public enum GocryptfsNetworkCache {
    public static let directory = AppPaths.networkCache

    private static let defaultMaxBytes: Int64 = 10 * 1_073_741_824  // 10 GB
    private static let maxBytesKey = "networkCacheMaxBytes"

    /// The limit chosen in Settings; 0 means unlimited.
    public static var maxBytes: Int64 {
        get { AppSettings.shared.int64(forKey: maxBytesKey) ?? defaultMaxBytes }
        set {
            AppSettings.shared.set(newValue, forKey: maxBytesKey)
            applyLimit()
        }
    }

    /// Hands the limit to the engine, which evicts down to it. Call it before
    /// the first vault opens; the engine does not remember it.
    public static func applyLimit() {
        let limit = maxBytes > 0 ? maxBytes : Int64.max
        _ = directory.path.withCString { GocryptfsNetworkCacheLimit(UnsafeMutablePointer(mutating: $0), limit) }
    }

    public static func size() -> Int64 {
        directory.path.withCString { GocryptfsNetworkCacheSize(UnsafeMutablePointer(mutating: $0)) }
    }

    public static func clear() throws {
        let rc = directory.path.withCString { GocryptfsNetworkCacheClear(UnsafeMutablePointer(mutating: $0)) }
        guard rc == 0 else { throw GocryptfsError.deleteFailed("network cache: " + GocryptfsBridge.describeWriteCode(rc)) }
    }
}

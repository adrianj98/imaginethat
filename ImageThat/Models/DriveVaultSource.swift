import AppKit
import AVFoundation

/// WritableMediaSource backed by a gocryptfs vault stored in Google Drive.
///
/// The Go engine opens the vault on Drive (`GoogleDriveService.openVault`) as
/// an ordinary writable session and does every gocryptfs read and write there
/// itself: encrypted names, directory IVs, long-name sidecars, streamed
/// resumable uploads. So reads, writes, video and thumbnails are exactly a
/// local vault's, and this source hands them to a `VaultSource` on the same
/// session. Decrypted content stays in memory; downloaded ciphertext is kept
/// in the engine's Drive cache (`GocryptfsNetworkCache`).
public final class DriveVaultSource: WritableMediaSource, @unchecked Sendable {
    public let bridge: GocryptfsBridge
    public let currentPath: String
    private let vault: VaultSource

    public var rootDisplayName: String { "Drive Vault" }

    public init(bridge: GocryptfsBridge, currentPath: String = "") {
        self.bridge = bridge
        self.currentPath = currentPath
        self.vault = VaultSource(bridge: bridge, currentPath: currentPath)
    }

    public func source(at relativePath: String) -> any MediaSource {
        DriveVaultSource(bridge: bridge, currentPath: relativePath)
    }

    // MARK: - MediaSource

    public func listMedia() async throws -> [MediaItem] {
        try await vault.listMedia()
    }

    public func listSubdirectories() async throws -> [SubdirectoryEntry] {
        try await vault.listSubdirectories()
    }

    public func loadImage(for item: MediaItem) async throws -> NSImage {
        try await vault.loadImage(for: item)
    }

    public func loadThumbnail(for item: MediaItem, size: CGSize) async throws -> NSImage {
        try await vault.loadThumbnail(for: item, size: size)
    }

    public func fileSize(for item: MediaItem) async throws -> Int64 {
        try await vault.fileSize(for: item)
    }

    public func readChunks(for item: MediaItem, chunkSize: Int) -> AsyncThrowingStream<Data, Error> {
        vault.readChunks(for: item, chunkSize: chunkSize)
    }

    public func createPlayerItem(for item: MediaItem) throws -> AVPlayerItem {
        try vault.createPlayerItem(for: item)
    }

    /// The ciphertext ranges of this file held in the Drive cache, as
    /// fractions of the file. Ciphertext and plaintext offsets differ only
    /// by gocryptfs' fixed per-block overhead, so the fractions carry over.
    public func prefetch(_ item: MediaItem) async { await vault.prefetch(item) }

    public func cachedRanges(for item: MediaItem) async -> [ClosedRange<Double>] {
        bridge.cachedRanges(path: item.url.path)
    }

    // MARK: - WritableMediaSource

    public func saveImage(_ imageData: Data, for item: MediaItem) async throws {
        try await vault.saveImage(imageData, for: item)
    }

    public func saveImageCopy(_ imageData: Data, for item: MediaItem, suffix: String) async throws -> MediaItem {
        try await vault.saveImageCopy(imageData, for: item, suffix: suffix)
    }

    public func deleteItem(_ item: MediaItem) async throws {
        try await vault.deleteItem(item)
        bridge.invalidateThumbnail(path: item.url.path)
    }

    public func moveItem(_ item: MediaItem, to destinationPath: String) async throws {
        try await vault.moveItem(item, to: destinationPath)
    }

    public func createDirectory(named name: String) async throws {
        try await vault.createDirectory(named: name)
    }

    public func deleteCurrentDirectory() async throws {
        try await vault.deleteCurrentDirectory()
    }

    public func writeChunks<S: AsyncSequence>(_ chunks: S, to destinationPath: String, totalSize: Int64) async throws
        where S.Element == Data {
        try await vault.writeChunks(chunks, to: destinationPath, totalSize: totalSize)
    }
}

extension GocryptfsBridge {
    /// The Drive folder ID of a Drive vault session's root.
    var driveFolderId: String { vaultPath.host ?? "" }
}

// MARK: - OfflineCaching

extension DriveVaultSource: OfflineCaching {
    public var canCache: Bool { true }

    /// Cached when the whole file is pinned in the Drive cache.
    public func isCached(_ item: MediaItem) async -> Bool {
        GocryptfsTransfer.isCached(session: bridge.sessionID, path: item.url.path)
    }

    /// The engine fetches the file's ciphertext into the Drive cache,
    /// retrying and resuming, and pins it there so the size limit in
    /// Settings never evicts it.
    public func cache(_ item: MediaItem, onProgress: @escaping @Sendable (Int64, Int64) -> Void) async throws {
        try await GocryptfsTransfer.cacheFile(session: bridge.sessionID, path: item.url.path, onProgress: onProgress)
    }

    public func uncache(_ item: MediaItem) async {
        GocryptfsTransfer.uncache(session: bridge.sessionID, path: item.url.path)
    }
}

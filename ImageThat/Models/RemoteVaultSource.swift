import AppKit
import AVFoundation
import ImageIO

/// Sidebar and pending-unlock URLs for imagethat-web servers:
///   imagethat-server://server?server=<base URL>            the server itself
///   imagethat-server://vault/<vault ID>?server=<base URL>  one of its vaults
public enum RemoteLocation {
    public static let scheme = "imagethat-server"

    public static func url(server: String, vaultID: String? = nil) -> URL {
        var components = URLComponents()
        components.scheme = scheme
        components.host = vaultID == nil ? "server" : "vault"
        if let vaultID { components.path = "/\(vaultID)" }
        components.queryItems = [URLQueryItem(name: "server", value: server)]
        return components.url!
    }

    public static func parse(_ url: URL) -> (server: String, vaultID: String?)? {
        guard url.scheme == scheme,
              let components = URLComponents(url: url, resolvingAgainstBaseURL: false),
              let server = components.queryItems?.first(where: { $0.name == "server" })?.value else { return nil }
        let id = String(components.path.dropFirst())
        return (server, components.host == "vault" && !id.isEmpty ? id : nil)
    }

    /// Key for remembered passwords; distinct from local vault paths and gdrive:// keys.
    public static func passwordKey(server: String, vaultID: String) -> String {
        url(server: server, vaultID: vaultID).absoluteString
    }
}

/// WritableMediaSource backed by a vault on an imagethat-web server.
///
/// The server decrypts and streams; video goes through EncryptedResourceLoader
/// with HTTP Range reads, so only the parts being watched cross the network.
public final class RemoteVaultSource: WritableMediaSource, @unchecked Sendable {
    public let client: RemoteServerClient
    public let vaultID: String
    public let vaultName: String
    public private(set) var currentPath: String
    /// A ciphertext-mode session on the same vault with a file cache. Cached
    /// files are read and decrypted through it, from disk; everything else
    /// comes from the server's decrypted API as before.
    public let cache: GocryptfsBridge?

    public var rootDisplayName: String { vaultName }

    public init(client: RemoteServerClient, vaultID: String, vaultName: String, currentPath: String = "",
                cache: GocryptfsBridge? = nil) {
        self.client = client
        self.vaultID = vaultID
        self.vaultName = vaultName
        self.currentPath = currentPath
        self.cache = cache
    }

    public func source(at relativePath: String) -> any MediaSource {
        RemoteVaultSource(client: client, vaultID: vaultID, vaultName: vaultName, currentPath: relativePath, cache: cache)
    }

    func withCache(_ cache: GocryptfsBridge) -> RemoteVaultSource {
        RemoteVaultSource(client: client, vaultID: vaultID, vaultName: vaultName, currentPath: currentPath, cache: cache)
    }

    /// The cache session when it holds this file.
    private func cached(_ item: MediaItem) -> GocryptfsBridge? {
        guard let cache, GocryptfsTransfer.isCached(session: cache.sessionID, path: item.url.path) else { return nil }
        return cache
    }

    private func dropCached(_ path: String) {
        if let cache { GocryptfsTransfer.uncache(session: cache.sessionID, path: path) }
    }

    // MARK: - MediaSource

    public func listMedia() async throws -> [MediaItem] {
        let entries = try await client.list(vaultID: vaultID, path: currentPath)
        return entries.compactMap { entry -> MediaItem? in
            guard !entry.isDir, !entry.name.hasPrefix(".") else { return nil }
            let ext = (entry.name as NSString).pathExtension.lowercased()
            guard SupportedFormats.allExtensions.contains(ext) else { return nil }
            let mediaType: MediaType = SupportedFormats.videoExtensions.contains(ext) ? .video : .image
            return MediaItem(name: entry.name, url: Self.itemURL(join(currentPath, entry.name)), mediaType: mediaType)
        }.sorted { $0.name.localizedCaseInsensitiveCompare($1.name) == .orderedAscending }
    }

    public func listSubdirectories() async throws -> [SubdirectoryEntry] {
        try await client.list(vaultID: vaultID, path: currentPath)
            .filter { $0.isDir && !$0.name.hasPrefix(".") }
            .sorted { $0.name.localizedCaseInsensitiveCompare($1.name) == .orderedAscending }
            .map { SubdirectoryEntry(name: $0.name, relativePath: join(currentPath, $0.name)) }
    }

    public func loadImage(for item: MediaItem) async throws -> NSImage {
        let data: Data
        if let local = cached(item) {
            data = try local.decryptFile(path: item.url.path)
        } else {
            data = try await client.fileData(vaultID: vaultID, path: item.url.path)
        }
        guard let image = NSImage(data: data) else { throw MediaError.failedToLoadImage(item.name) }
        return image
    }

    public func loadThumbnail(for item: MediaItem, size: CGSize) async throws -> NSImage {
        let pixels = Int(max(size.width, size.height) * 2)
        if let jpeg = try await client.thumbnail(vaultID: vaultID, path: item.url.path,
                                                 size: min(max(pixels, 32), 2048)),
           let image = NSImage(data: jpeg) {
            return image
        }
        // The server could not decode this one (HEIC, RAW, or video without
        // ffmpeg there), so render it here.
        if item.mediaType == .video {
            return try await loadVideoThumbnail(for: item, size: size)
        }
        let data = try await client.fileData(vaultID: vaultID, path: item.url.path)
        guard let source = CGImageSourceCreateWithData(data as CFData, nil),
              let cgImage = CGImageSourceCreateThumbnailAtIndex(source, 0, [
                  kCGImageSourceCreateThumbnailFromImageAlways: true,
                  kCGImageSourceThumbnailMaxPixelSize: pixels,
                  kCGImageSourceCreateThumbnailWithTransform: true,
              ] as CFDictionary) else {
            throw MediaError.failedToLoadImage(item.name)
        }
        return NSImage(cgImage: cgImage, size: NSSize(width: cgImage.width, height: cgImage.height))
    }

    public func fileSize(for item: MediaItem) async throws -> Int64 {
        if let local = cached(item) { return try local.fileSize(path: item.url.path) }
        return try await client.fileSize(vaultID: vaultID, path: item.url.path)
    }

    public func readChunks(for item: MediaItem, chunkSize: Int) -> AsyncThrowingStream<Data, Error> {
        let client = self.client, vaultID = self.vaultID, path = item.url.path
        let local = cached(item)
        return AsyncThrowingStream { continuation in
            let task = Task {
                do {
                    var offset: Int64 = 0
                    while true {
                        let chunk: Data
                        if let local {
                            chunk = try local.readRange(path: path, offset: offset, length: Int32(chunkSize))
                        } else {
                            chunk = try await client.readRange(vaultID: vaultID, path: path,
                                                               offset: offset, length: chunkSize)
                        }
                        if chunk.isEmpty { break }
                        offset += Int64(chunk.count)
                        continuation.yield(chunk)
                        if chunk.count < chunkSize { break }
                    }
                    continuation.finish()
                } catch {
                    continuation.finish(throwing: error)
                }
            }
            continuation.onTermination = { _ in task.cancel() }
        }
    }

    public func createPlayerItem(for item: MediaItem) throws -> AVPlayerItem {
        let reader: any EncryptedRangeReading = cached(item) ?? RemoteRangeReader(client: client, vaultID: vaultID)
        let loader = EncryptedResourceLoader(bridge: reader, relativePath: item.url.path)
        let asset = try makeAsset(for: item, loader: loader, queueLabel: "com.imagethat.remoteloader")
        let playerItem = AVPlayerItem(asset: asset)
        objc_setAssociatedObject(playerItem, "resourceLoader", loader, .OBJC_ASSOCIATION_RETAIN)
        return playerItem
    }

    // MARK: - WritableMediaSource

    public func saveImage(_ imageData: Data, for item: MediaItem) async throws {
        // The server never overwrites, so write a hidden sibling and swap it
        // in. Should the final rename fail, the edit survives under that name.
        let path = item.url.path
        dropCached(path)
        let dir = parentDirectory(path)
        let temp = ".imagethat-save-\(UUID().uuidString)-\(item.name)"
        try await client.upload(vaultID: vaultID, directory: dir, name: temp, data: imageData)
        try await client.delete(vaultID: vaultID, path: path)
        try await client.move(vaultID: vaultID, from: join(dir, temp), to: path)
    }

    public func saveImageCopy(_ imageData: Data, for item: MediaItem, suffix: String) async throws -> MediaItem {
        let ext = (item.name as NSString).pathExtension
        let newName = (item.name as NSString).deletingPathExtension + suffix + (ext.isEmpty ? "" : ".\(ext)")
        let dir = parentDirectory(item.url.path)
        try await client.upload(vaultID: vaultID, directory: dir, name: newName, data: imageData)
        return MediaItem(name: newName, url: Self.itemURL(join(dir, newName)), mediaType: item.mediaType)
    }

    public func deleteItem(_ item: MediaItem) async throws {
        dropCached(item.url.path)
        try await client.delete(vaultID: vaultID, path: item.url.path)
    }

    /// `destinationPath` is a directory, as for VaultSource.
    public func moveItem(_ item: MediaItem, to destinationPath: String) async throws {
        // The cache is keyed by path; the copy would be orphaned by the move.
        dropCached(item.url.path)
        try await client.move(vaultID: vaultID, from: item.url.path, to: join(destinationPath, item.name))
    }

    public func createDirectory(named name: String) async throws {
        try await client.mkdir(vaultID: vaultID, path: join(currentPath, name))
    }

    public func deleteCurrentDirectory() async throws {
        try await client.delete(vaultID: vaultID, path: currentPath)
    }

    public func writeChunks<S: AsyncSequence>(_ chunks: S, to destinationPath: String, totalSize: Int64) async throws
        where S.Element == Data {
        let startTime = Date()
        let progressName = NSNotification.Name("com.adrianm.imagethat.fileTransferProgress")
        try await client.upload(
            vaultID: vaultID,
            directory: parentDirectory(destinationPath),
            name: (destinationPath as NSString).lastPathComponent,
            chunks: chunks
        ) { sent in
            NotificationCenter.default.post(name: progressName, object: (current: sent, total: totalSize, startTime: startTime))
        }
        NotificationCenter.default.post(name: progressName, object: (current: totalSize, total: totalSize, startTime: startTime))
    }

    // MARK: - Private

    private func loadVideoThumbnail(for item: MediaItem, size: CGSize) async throws -> NSImage {
        let loader = EncryptedResourceLoader(bridge: RemoteRangeReader(client: client, vaultID: vaultID),
                                             relativePath: item.url.path)
        let asset = try makeAsset(for: item, loader: loader, queueLabel: "com.imagethat.remotethumb")
        let generator = AVAssetImageGenerator(asset: asset)
        generator.maximumSize = CGSize(width: size.width * 2, height: size.height * 2)
        generator.appliesPreferredTrackTransform = true
        let cgImage = try await generator.image(at: CMTime(seconds: 1, preferredTimescale: 600)).image
        return NSImage(cgImage: cgImage, size: NSSize(width: cgImage.width, height: cgImage.height))
    }

    private func makeAsset(for item: MediaItem, loader: EncryptedResourceLoader, queueLabel: String) throws -> AVURLAsset {
        var components = URLComponents()
        components.scheme = "imagethat-remote"
        components.host = "vault"
        components.path = "/\(item.url.path)"
        guard let url = components.url else { throw MediaError.failedToLoadVideo(item.name) }
        let asset = AVURLAsset(url: url)
        asset.resourceLoader.setDelegate(loader, queue: DispatchQueue(label: queueLabel))
        return asset
    }

    private func join(_ dir: String, _ name: String) -> String {
        dir.isEmpty ? name : "\(dir)/\(name)"
    }

    private func parentDirectory(_ path: String) -> String {
        let dir = (path as NSString).deletingLastPathComponent
        return dir == "." ? "" : dir
    }

    /// Item URLs carry the vault-relative path, matching DriveVaultSource.
    static func itemURL(_ path: String) -> URL {
        URL(string: path.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? path)!
    }
}

// MARK: - OfflineCaching

extension RemoteVaultSource: OfflineCaching {
    public var canCache: Bool { cache != nil }

    public func isCached(_ item: MediaItem) async -> Bool { cached(item) != nil }

    public func cache(_ item: MediaItem, onProgress: @escaping @Sendable (Int64, Int64) -> Void) async throws {
        guard let cache else { throw MediaError.failedToLoadImage(item.name) }
        try await GocryptfsTransfer.cacheFile(session: cache.sessionID, path: item.url.path, onProgress: onProgress)
    }

    public func uncache(_ item: MediaItem) async { dropCached(item.url.path) }

    public func cachedRanges(for item: MediaItem) async -> [ClosedRange<Double>] {
        cached(item) == nil ? [] : [0...1]
    }
}

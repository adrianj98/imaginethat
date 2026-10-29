import AppKit
import AVFoundation
import ImageIO

public final class VaultSource: WritableMediaSource, @unchecked Sendable {
    public let bridge: GocryptfsBridge
    public private(set) var currentPath: String

    public var rootDisplayName: String {
        bridge.vaultPath.lastPathComponent
    }

    public init(bridge: GocryptfsBridge, currentPath: String = "") {
        self.bridge = bridge
        self.currentPath = currentPath
    }

    /// Create a new VaultSource pointing to a subdirectory within the same vault
    public func navigatingTo(path: String) -> VaultSource {
        VaultSource(bridge: bridge, currentPath: path)
    }

    public func source(at relativePath: String) -> any MediaSource {
        VaultSource(bridge: bridge, currentPath: relativePath)
    }

    public func listMedia() async throws -> [MediaItem] {
        let entries = try bridge.listDirectory(path: currentPath)
        var items: [MediaItem] = []

        for entry in entries {
            if entry.isDirectory { continue }
            if entry.name.hasPrefix("._") || entry.name.hasPrefix(".") { continue }

            let ext = (entry.name as NSString).pathExtension.lowercased()
            guard SupportedFormats.allExtensions.contains(ext) else { continue }

            let entryPath = currentPath.isEmpty ? entry.name : "\(currentPath)/\(entry.name)"
            let url = URL(string: entryPath.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? entryPath)!
            let mediaType: MediaType = SupportedFormats.videoExtensions.contains(ext) ? .video : .image
            items.append(MediaItem(name: entry.name, url: url, mediaType: mediaType))
        }

        return items.sorted { $0.name.localizedCaseInsensitiveCompare($1.name) == .orderedAscending }
    }

    /// List subdirectories at the current path
    public func listSubdirectories() async throws -> [SubdirectoryEntry] {
        let entries = try bridge.listDirectory(path: currentPath)
        return entries
            .filter { $0.isDirectory && !$0.name.hasPrefix(".") }
            .sorted { $0.name.localizedCaseInsensitiveCompare($1.name) == .orderedAscending }
            .map { entry in
                let subPath = currentPath.isEmpty ? entry.name : "\(currentPath)/\(entry.name)"
                return SubdirectoryEntry(name: entry.name, relativePath: subPath)
            }
    }

    /// List subdirectories at a given path within the vault (legacy method for internal use)
    func listSubdirectories(path: String) throws -> [(name: String, path: String)] {
        let entries = try bridge.listDirectory(path: path)
        return entries
            .filter { $0.isDirectory && !$0.name.hasPrefix(".") }
            .sorted { $0.name.localizedCaseInsensitiveCompare($1.name) == .orderedAscending }
            .map { entry in
                let subPath = path.isEmpty ? entry.name : "\(path)/\(entry.name)"
                return (name: entry.name, path: subPath)
            }
    }

    public func loadImage(for item: MediaItem) async throws -> NSImage {
        let data = try readFileInChunks(path: item.url.path)
        guard let image = NSImage(data: data) else {
            throw MediaError.failedToLoadImage(item.name)
        }
        return image
    }

    public func loadThumbnail(for item: MediaItem, size: CGSize) async throws -> NSImage {
        if item.mediaType == .video {
            return try await loadVideoThumbnail(for: item, size: size)
        }
        return try loadImageThumbnail(for: item, size: size)
    }

    public func fileSize(for item: MediaItem) async throws -> Int64 {
        try bridge.fileSize(path: item.url.path)
    }

    public func readChunks(for item: MediaItem, chunkSize: Int) -> AsyncThrowingStream<Data, Error> {
        AsyncThrowingStream { continuation in
            do {
                let size = try bridge.fileSize(path: item.url.path)
                var offset: Int64 = 0
                while offset < size {
                    let toRead = min(Int32(chunkSize), Int32(size - offset))
                    let chunk = try bridge.readRange(path: item.url.path, offset: offset, length: toRead)
                    if chunk.isEmpty { break }
                    offset += Int64(chunk.count)
                    continuation.yield(chunk)
                    if chunk.count < Int(toRead) { break }
                }
                continuation.finish()
            } catch {
                continuation.finish(throwing: error)
            }
        }
    }

    public func createPlayerItem(for item: MediaItem) throws -> AVPlayerItem {
        let resourceLoader = EncryptedResourceLoader(bridge: bridge, relativePath: item.url.path)
        var components = URLComponents()
        components.scheme = "imagethat-encrypted"
        components.host = "vault"
        components.path = "/\(item.url.path)"
        guard let url = components.url else {
            throw MediaError.failedToLoadVideo(item.name)
        }
        let asset = AVURLAsset(url: url)
        asset.resourceLoader.setDelegate(resourceLoader, queue: DispatchQueue(label: "com.imagethat.resourceloader"))
        let playerItem = AVPlayerItem(asset: asset)
        objc_setAssociatedObject(playerItem, "resourceLoader", resourceLoader, .OBJC_ASSOCIATION_RETAIN)
        return playerItem
    }

    // MARK: - Private

    /// Read an entire file using chunked readRange calls to avoid 3x memory overhead of decryptFile.
    private func readFileInChunks(path: String) throws -> Data {
        let fileSize = try bridge.fileSize(path: path)
        guard fileSize > 0 else { return Data() }

        var data = Data()
        data.reserveCapacity(Int(fileSize))

        let chunkSize: Int32 = 256 * 1024 // 256KB
        var offset: Int64 = 0

        while offset < fileSize {
            let toRead = Int32(min(Int64(chunkSize), fileSize - offset))
            let chunk = try bridge.readRange(path: path, offset: offset, length: toRead)
            if chunk.isEmpty { break }
            data.append(chunk)
            offset += Int64(chunk.count)
            if chunk.count < Int(toRead) { break }
        }

        return data
    }

    private func loadImageThumbnail(for item: MediaItem, size: CGSize) throws -> NSImage {
        let data = try readFileInChunks(path: item.url.path)
        let maxDimension = max(size.width, size.height) * 2

        guard let source = CGImageSourceCreateWithData(data as CFData, nil) else {
            throw MediaError.failedToLoadImage(item.name)
        }

        let options: [CFString: Any] = [
            kCGImageSourceCreateThumbnailFromImageAlways: true,
            kCGImageSourceThumbnailMaxPixelSize: maxDimension,
            kCGImageSourceCreateThumbnailWithTransform: true
        ]

        guard let cgImage = CGImageSourceCreateThumbnailAtIndex(source, 0, options as CFDictionary) else {
            throw MediaError.failedToLoadImage(item.name)
        }

        return NSImage(cgImage: cgImage, size: NSSize(width: cgImage.width, height: cgImage.height))
    }

    private func loadVideoThumbnail(for item: MediaItem, size: CGSize) async throws -> NSImage {
        // Use the streaming resource loader to get a thumbnail without decrypting the entire file.
        // AVAssetImageGenerator will only read the bytes it needs via the resource loader.
        let resourceLoader = EncryptedResourceLoader(bridge: bridge, relativePath: item.url.path)
        var components = URLComponents()
        components.scheme = "imagethat-encrypted"
        components.host = "vault"
        components.path = "/\(item.url.path)"
        guard let url = components.url else {
            throw MediaError.failedToLoadVideo(item.name)
        }
        let asset = AVURLAsset(url: url)
        asset.resourceLoader.setDelegate(resourceLoader, queue: DispatchQueue(label: "com.imagethat.videothumb"))

        let generator = AVAssetImageGenerator(asset: asset)
        generator.maximumSize = CGSize(width: size.width * 2, height: size.height * 2)
        generator.appliesPreferredTrackTransform = true

        let time = CMTime(seconds: 1, preferredTimescale: 600)
        let cgImage = try generator.copyCGImage(at: time, actualTime: nil)
        return NSImage(cgImage: cgImage, size: NSSize(width: cgImage.width, height: cgImage.height))
    }

    public func saveImage(_ imageData: Data, for item: MediaItem) async throws {
        try bridge.writeFile(path: item.url.path, data: imageData)
    }

    public func saveImageCopy(_ imageData: Data, for item: MediaItem, suffix: String) async throws -> MediaItem {
        let originalPath = item.url.path
        let fileExtension = (originalPath as NSString).pathExtension
        let fileName = ((originalPath as NSString).lastPathComponent as NSString).deletingPathExtension

        let newFileName = fileName + suffix + "." + fileExtension
        let newPath: String
        if let lastSlash = originalPath.range(of: "/", options: .backwards) {
            let directory = String(originalPath[..<lastSlash.lowerBound])
            newPath = directory.isEmpty ? newFileName : directory + "/" + newFileName
        } else {
            newPath = newFileName
        }

        try bridge.writeFile(path: newPath, data: imageData)

        let newURL = URL(string: newPath.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? newPath)!
        return MediaItem(name: newFileName, url: newURL, mediaType: item.mediaType)
    }

    public func deleteItem(_ item: MediaItem) async throws {
        try bridge.deleteFile(path: item.url.path)
    }

    public func moveItem(_ item: MediaItem, to destinationPath: String) async throws {
        try bridge.moveFile(fromPath: item.url.path, toDirectory: destinationPath)
    }

    public func createDirectory(named name: String) async throws {
        let newPath = currentPath.isEmpty ? name : "\(currentPath)/\(name)"
        try bridge.mkdir(path: newPath)
    }

    public func deleteCurrentDirectory() async throws {
        try bridge.deleteDirectory(path: currentPath)
    }

    public func writeChunks<S: AsyncSequence>(_ chunks: S, to destinationPath: String, totalSize: Int64) async throws
        where S.Element == Data {
        // Stream through the Go engine: each chunk is encrypted as it arrives
        // and only ciphertext reaches the vault directory, with memory bounded
        // by the chunk size. The entry appears when the writer finishes; an
        // error or cancellation part-way discards it, leaving nothing behind.
        let writer = try bridge.createFile(path: destinationPath)

        let startTime = Date()
        var totalWritten: Int64 = 0
        var lastReportTime = Date()

        do {
            for try await chunk in chunks {
                try Task.checkCancellation()
                try writer.write(chunk)
                totalWritten += Int64(chunk.count)

                // Report progress every 0.5 seconds
                let now = Date()
                if now.timeIntervalSince(lastReportTime) >= 0.5 {
                    lastReportTime = now
                    NotificationCenter.default.post(
                        name: NSNotification.Name("com.adrianm.imagethat.fileTransferProgress"),
                        object: (current: totalWritten, total: totalSize, startTime: startTime)
                    )
                }
            }
            try writer.finish()
        } catch {
            writer.abort()
            throw error
        }

        // Final progress update
        NotificationCenter.default.post(
            name: NSNotification.Name("com.adrianm.imagethat.fileTransferProgress"),
            object: (current: totalSize, total: totalSize, startTime: startTime)
        )
    }
}

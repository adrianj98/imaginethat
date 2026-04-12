import AppKit
import AVFoundation
import UniformTypeIdentifiers
import ImageIO

public final class FolderSource: WritableMediaSource, @unchecked Sendable {
    public let rootURL: URL
    public let currentPath: String

    public var rootDisplayName: String {
        rootURL.lastPathComponent
    }

    public init(rootURL: URL, currentPath: String = "") {
        self.rootURL = rootURL
        self.currentPath = currentPath
    }

    public func source(at relativePath: String) -> any MediaSource {
        FolderSource(rootURL: rootURL.appendingPathComponent(relativePath), currentPath: relativePath)
    }

    public func listMedia() async throws -> [MediaItem] {
        let fm = FileManager.default
        let folderURL = rootURL.appendingPathComponent(currentPath)
        let contents = try fm.contentsOfDirectory(
            at: folderURL,
            includingPropertiesForKeys: [.isRegularFileKey, .contentTypeKey],
            options: [.skipsHiddenFiles]
        )

        return contents
            .filter { url in
                let ext = url.pathExtension.lowercased()
                return SupportedFormats.imageExtensions.contains(ext) ||
                       SupportedFormats.videoExtensions.contains(ext)
            }
            .sorted { $0.lastPathComponent.localizedCaseInsensitiveCompare($1.lastPathComponent) == .orderedAscending }
            .map { MediaItem(url: $0) }
    }

    public func listSubdirectories() async throws -> [SubdirectoryEntry] {
        let fm = FileManager.default
        let folderURL = rootURL.appendingPathComponent(currentPath)
        let contents = try fm.contentsOfDirectory(
            at: folderURL,
            includingPropertiesForKeys: [.isDirectoryKey],
            options: [.skipsHiddenFiles]
        )

        return contents
            .filter { url in
                let isDir = (try? url.resourceValues(forKeys: [.isDirectoryKey]).isDirectory) == true
                guard isDir else { return false }
                // Skip vault directories
                let hasGocryptfs = fm.fileExists(atPath: url.appendingPathComponent("gocryptfs.conf").path)
                return !hasGocryptfs
            }
            .sorted { $0.lastPathComponent.localizedCaseInsensitiveCompare($1.lastPathComponent) == .orderedAscending }
            .map { url in
                let relativePath = currentPath.isEmpty ? url.lastPathComponent : "\(currentPath)/\(url.lastPathComponent)"
                return SubdirectoryEntry(name: url.lastPathComponent, relativePath: relativePath)
            }
    }

    public func loadImage(for item: MediaItem) async throws -> NSImage {
        guard let image = NSImage(contentsOf: item.url) else {
            throw MediaError.failedToLoadImage(item.name)
        }
        return image
    }

    public func loadThumbnail(for item: MediaItem, size: CGSize) async throws -> NSImage {
        if item.mediaType == .video {
            return try await loadVideoThumbnail(for: item, size: size)
        }
        return try await loadImageThumbnail(for: item, size: size)
    }

    public func createPlayerItem(for item: MediaItem) throws -> AVPlayerItem {
        AVPlayerItem(url: item.url)
    }

    public func fileSize(for item: MediaItem) async throws -> Int64 {
        let attrs = try FileManager.default.attributesOfItem(atPath: item.url.path)
        return attrs[.size] as? Int64 ?? 0
    }

    public func readChunks(for item: MediaItem, chunkSize: Int) -> AsyncThrowingStream<Data, Error> {
        AsyncThrowingStream { continuation in
            do {
                let handle = try FileHandle(forReadingFrom: item.url)
                defer { try? handle.close() }
                while true {
                    guard let chunk = try handle.read(upToCount: chunkSize), !chunk.isEmpty else { break }
                    continuation.yield(chunk)
                }
                continuation.finish()
            } catch {
                continuation.finish(throwing: error)
            }
        }
    }

    private func loadImageThumbnail(for item: MediaItem, size: CGSize) async throws -> NSImage {
        let maxDimension = max(size.width, size.height) * 2 // retina

        guard let source = CGImageSourceCreateWithURL(item.url as CFURL, nil) else {
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
        let asset = AVURLAsset(url: item.url)
        let generator = AVAssetImageGenerator(asset: asset)
        generator.maximumSize = CGSize(width: size.width * 2, height: size.height * 2)
        generator.appliesPreferredTrackTransform = true

        let time = CMTime(seconds: 1, preferredTimescale: 600)
        let cgImage = try generator.copyCGImage(at: time, actualTime: nil)
        return NSImage(cgImage: cgImage, size: NSSize(width: cgImage.width, height: cgImage.height))
    }

    public func saveImage(_ imageData: Data, for item: MediaItem) async throws {
        try imageData.write(to: item.url)
    }

    public func saveImageCopy(_ imageData: Data, for item: MediaItem, suffix: String) async throws -> MediaItem {
        let originalURL = item.url
        let fileExtension = originalURL.pathExtension
        let fileName = originalURL.deletingPathExtension().lastPathComponent

        let newFileName = fileName + suffix + "." + fileExtension
        let newURL = originalURL.deletingLastPathComponent().appendingPathComponent(newFileName)

        try imageData.write(to: newURL)
        return MediaItem(url: newURL)
    }

    public func deleteItem(_ item: MediaItem) async throws {
        try FileManager.default.removeItem(at: item.url)
    }

    public func moveItem(_ item: MediaItem, to destinationPath: String) async throws {
        let sourceFileName = item.url.lastPathComponent
        let destinationURL = rootURL.appendingPathComponent(destinationPath).appendingPathComponent(sourceFileName)
        try FileManager.default.moveItem(at: item.url, to: destinationURL)
    }

    public func createDirectory(named name: String) async throws {
        let folderURL = rootURL.appendingPathComponent(currentPath)
        let newDirURL = folderURL.appendingPathComponent(name)
        try FileManager.default.createDirectory(at: newDirURL, withIntermediateDirectories: true)
    }

    public func deleteCurrentDirectory() async throws {
        let folderURL = rootURL.appendingPathComponent(currentPath)
        try FileManager.default.removeItem(at: folderURL)
    }

    public func writeChunks<S: AsyncSequence>(_ chunks: S, to destinationPath: String, totalSize: Int64) async throws
        where S.Element == Data {
        // Post "writing" phase notification
        NotificationCenter.default.post(
            name: NSNotification.Name("com.adrianm.imagethat.fileTransferPhase"),
            object: "writing"
        )

        let dest = rootURL.appendingPathComponent(destinationPath)
        FileManager.default.createFile(atPath: dest.path, contents: nil)
        let handle = try FileHandle(forWritingTo: dest)
        defer { try? handle.close() }

        let startTime = Date()
        var totalWritten: Int64 = 0
        var lastReportTime = Date()

        for try await chunk in chunks {
            try handle.write(contentsOf: chunk)
            totalWritten += Int64(chunk.count)

            // Report progress every 0.5 seconds while writing
            let now = Date()
            if now.timeIntervalSince(lastReportTime) >= 0.5 {
                lastReportTime = now
                NotificationCenter.default.post(
                    name: NSNotification.Name("com.adrianm.imagethat.fileTransferProgress"),
                    object: (current: totalWritten, total: totalSize, startTime: startTime)
                )
            }
        }

        // Report completion
        NotificationCenter.default.post(
            name: NSNotification.Name("com.adrianm.imagethat.fileTransferPhase"),
            object: nil
        )
    }
}

public enum MediaError: LocalizedError {
    case failedToLoadImage(String)
    case failedToLoadVideo(String)
    case failedToSaveImage(String)
    case vaultNotUnlocked
    case decryptionFailed(String)

    public var errorDescription: String? {
        switch self {
        case .failedToLoadImage(let name): return "Failed to load image: \(name)"
        case .failedToLoadVideo(let name): return "Failed to load video: \(name)"
        case .failedToSaveImage(let msg): return "Failed to save image: \(msg)"
        case .vaultNotUnlocked: return "Vault is not unlocked"
        case .decryptionFailed(let msg): return "Decryption failed: \(msg)"
        }
    }
}

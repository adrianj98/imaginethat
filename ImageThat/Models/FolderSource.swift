import AppKit
import AVFoundation
import UniformTypeIdentifiers
import ImageIO

public final class FolderSource: MediaSource, @unchecked Sendable {
    public let rootURL: URL

    public var rootDisplayName: String {
        rootURL.lastPathComponent
    }

    public init(rootURL: URL) {
        self.rootURL = rootURL
    }

    public func listMedia() async throws -> [MediaItem] {
        let fm = FileManager.default
        let contents = try fm.contentsOfDirectory(
            at: rootURL,
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

    public func videoURL(for item: MediaItem) async throws -> URL {
        item.url
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
}

public enum MediaError: LocalizedError {
    case failedToLoadImage(String)
    case failedToLoadVideo(String)
    case vaultNotUnlocked
    case decryptionFailed(String)

    public var errorDescription: String? {
        switch self {
        case .failedToLoadImage(let name): return "Failed to load image: \(name)"
        case .failedToLoadVideo(let name): return "Failed to load video: \(name)"
        case .vaultNotUnlocked: return "Vault is not unlocked"
        case .decryptionFailed(let msg): return "Decryption failed: \(msg)"
        }
    }
}

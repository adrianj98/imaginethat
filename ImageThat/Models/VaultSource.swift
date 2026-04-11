import AppKit
import AVFoundation
import ImageIO

public final class VaultSource: MediaSource, @unchecked Sendable {
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

    /// List subdirectories at a given path within the vault
    public func listSubdirectories(path: String = "") throws -> [(name: String, path: String)] {
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
        let data = try bridge.decryptFile(path: item.url.path)
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

    public func videoURL(for item: MediaItem) async throws -> URL {
        var components = URLComponents()
        components.scheme = "imagethat-encrypted"
        components.path = item.url.path
        guard let url = components.url else {
            throw MediaError.failedToLoadVideo(item.name)
        }
        return url
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

    private func loadImageThumbnail(for item: MediaItem, size: CGSize) throws -> NSImage {
        let data = try bridge.decryptFile(path: item.url.path)
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
}

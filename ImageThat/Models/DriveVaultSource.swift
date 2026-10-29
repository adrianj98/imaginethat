import AppKit
import AVFoundation
import ImageIO
import Security
import UniformTypeIdentifiers

/// WritableMediaSource backed by a gocryptfs vault stored in Google Drive.
/// Decrypted content stays in memory; encrypted Drive files are cached via DriveCache.
public final class DriveVaultSource: WritableMediaSource, @unchecked Sendable {
    public let bridge: DriveVaultBridge
    public private(set) var currentPath: String

    public var rootDisplayName: String { "Drive Vault" }

    public init(bridge: DriveVaultBridge, currentPath: String = "") {
        self.bridge = bridge
        self.currentPath = currentPath
    }

    public func source(at relativePath: String) -> any MediaSource {
        DriveVaultSource(bridge: bridge, currentPath: relativePath)
    }

    // MARK: - MediaSource

    public func listMedia() async throws -> [MediaItem] {
        let entries = try bridge.listDirectory(path: currentPath)
        return entries.compactMap { entry -> MediaItem? in
            guard !entry.isDirectory else { return nil }
            guard !entry.name.hasPrefix(".") else { return nil }
            let ext = (entry.name as NSString).pathExtension.lowercased()
            guard SupportedFormats.allExtensions.contains(ext) else { return nil }
            let entryPath = currentPath.isEmpty ? entry.name : "\(currentPath)/\(entry.name)"
            let url = URL(string: entryPath.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? entryPath)!
            let mediaType: MediaType = SupportedFormats.videoExtensions.contains(ext) ? .video : .image
            return MediaItem(name: entry.name, url: url, mediaType: mediaType)
        }.sorted { $0.name.localizedCaseInsensitiveCompare($1.name) == .orderedAscending }
    }

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
                    let toRead = Int32(min(Int64(chunkSize), size - offset))
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
        let loader = DriveVaultResourceLoader(bridge: bridge, relativePath: item.url.path)
        var components = URLComponents()
        components.scheme = "imagethat-drivevault"
        components.host = "vault"
        components.path = "/\(item.url.path)"
        guard let url = components.url else {
            throw MediaError.failedToLoadVideo(item.name)
        }
        let asset = AVURLAsset(url: url)
        asset.resourceLoader.setDelegate(loader, queue: DispatchQueue(label: "com.imagethat.drivevaultloader"))
        let playerItem = AVPlayerItem(asset: asset)
        objc_setAssociatedObject(playerItem, "resourceLoader", loader, .OBJC_ASSOCIATION_RETAIN)
        return playerItem
    }

    // MARK: - WritableMediaSource

    public func saveImage(_ imageData: Data, for item: MediaItem) async throws {
        guard let fileId = bridge.driveFileId(forVaultPath: item.url.path) else {
            throw GocryptfsError.writeFailed("Cannot locate Drive file ID for \(item.name)")
        }
        let ciphertext = try bridge.encryptData(imageData)
        try await GoogleDriveService.shared.updateFile(fileId: fileId, data: ciphertext, mimeType: "application/octet-stream")
        await DriveCache.shared.invalidate(fileId: fileId)
        DriveRangeCache.shared.invalidate(fileId: fileId)
    }

    public func saveImageCopy(_ imageData: Data, for item: MediaItem, suffix: String) async throws -> MediaItem {
        let originalPath = item.url.path
        let ext = (originalPath as NSString).pathExtension
        let baseName = ((originalPath as NSString).lastPathComponent as NSString).deletingPathExtension
        let newPlainName = baseName + suffix + "." + ext

        let plainDir: String
        let newPlainPath: String
        if let slash = originalPath.range(of: "/", options: .backwards) {
            plainDir = String(originalPath[..<slash.lowerBound])
            newPlainPath = plainDir + "/" + newPlainName
        } else {
            plainDir = ""
            newPlainPath = newPlainName
        }

        guard let parentFolderId = bridge.driveFolderId(forVaultDir: plainDir.isEmpty ? "" : plainDir),
              let cipherDir = bridge.cipherPath(forPlainPath: plainDir.isEmpty ? "" : plainDir, isDir: true),
              let cipherName = bridge.encryptFileName(cipherDirPath: cipherDir, plainName: newPlainName) else {
            throw GocryptfsError.writeFailed("Cannot resolve destination for \(newPlainName)")
        }

        let ciphertext = try bridge.encryptData(imageData)
        try await uploadEncrypted(ciphertext, cipherName: cipherName, plainName: newPlainName,
                                  cipherDir: cipherDir, parentFolderId: parentFolderId)

        let url = URL(string: newPlainPath.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? newPlainPath)!
        return MediaItem(name: newPlainName, url: url, mediaType: item.mediaType)
    }

    public func deleteItem(_ item: MediaItem) async throws {
        guard let fileId = bridge.driveFileId(forVaultPath: item.url.path) else {
            throw GocryptfsError.deleteFailed("Cannot locate Drive file ID for \(item.name)")
        }
        try await GoogleDriveService.shared.deleteFile(fileId: fileId)
        await DriveCache.shared.invalidate(fileId: fileId)
        DriveRangeCache.shared.invalidate(fileId: fileId)
        bridge.invalidateThumbnail(path: item.url.path)
        let parent = (item.url.path as NSString).deletingLastPathComponent
        if let cipherDir = bridge.cipherPath(forPlainPath: parent == "." ? "" : parent, isDir: true) {
            bridge.fs.invalidateListing(cipherDir: cipherDir)
        }
    }

    public func moveItem(_ item: MediaItem, to destinationPath: String) async throws {
        guard let fileId = bridge.driveFileId(forVaultPath: item.url.path),
              let entry = bridge.fs.getFileEntry(path: bridge.cipherPath(forPlainPath: item.url.path) ?? "") else {
            throw GocryptfsError.moveFailed("Cannot locate Drive file ID for \(item.name)")
        }
        guard let destFolderId = bridge.driveFolderId(forVaultDir: destinationPath),
              let srcFolderId = bridge.driveFolderId(forVaultDir: (item.url.path as NSString).deletingLastPathComponent) else {
            throw GocryptfsError.moveFailed("Cannot resolve destination folder")
        }
        let _ = entry
        try await GoogleDriveService.shared.moveFile(fileId: fileId, from: srcFolderId, to: destFolderId)
    }

    /// Creates a directory the way gocryptfs does: the Drive folder is named
    /// with the encrypted name, holds a fresh random gocryptfs.diriv, and a long
    /// name gets its .name sidecar. Without these the folder is invisible to
    /// the vault (and to gocryptfs) and its real name is readable on Drive.
    public func createDirectory(named name: String) async throws {
        guard let parentFolderId = bridge.driveFolderId(forVaultDir: currentPath),
              let cipherParent = bridge.cipherPath(forPlainPath: currentPath, isDir: true),
              let cipherName = bridge.encryptFileName(cipherDirPath: cipherParent, plainName: name) else {
            throw GocryptfsError.mkdirFailed("Cannot resolve parent folder")
        }
        let sidecar = try bridge.longNameSidecar(cipherDirPath: cipherParent, plainName: name)
        let drive = GoogleDriveService.shared

        var iv = Data(count: 16)
        let status = iv.withUnsafeMutableBytes { SecRandomCopyBytes(kSecRandomDefault, 16, $0.baseAddress!) }
        guard status == errSecSuccess else { throw GocryptfsError.mkdirFailed("no randomness for the directory IV") }

        let folder = try await drive.createFolder(name: cipherName, parentId: parentFolderId)
        do {
            _ = try await drive.uploadFile(name: "gocryptfs.diriv", parentId: folder.id,
                                           data: iv, mimeType: "application/octet-stream")
            if let sidecar {
                _ = try await drive.uploadFile(name: cipherName + ".name", parentId: parentFolderId,
                                               data: Data(sidecar.utf8), mimeType: "application/octet-stream")
            }
        } catch {
            // A folder without its IV is unreadable to gocryptfs; do not leave one.
            try? await drive.deleteFile(fileId: folder.id)
            throw error
        }
        bridge.fs.invalidateListing(cipherDir: cipherParent)
    }

    /// Uploads one encrypted file into a vault directory, with the long-name
    /// sidecar gocryptfs needs, and refreshes the Drive index for that directory.
    private func uploadEncrypted(_ ciphertext: Data, cipherName: String, plainName: String,
                                 cipherDir: String, parentFolderId: String) async throws {
        let drive = GoogleDriveService.shared
        if let sidecar = try bridge.longNameSidecar(cipherDirPath: cipherDir, plainName: plainName) {
            _ = try await drive.uploadFile(name: cipherName + ".name", parentId: parentFolderId,
                                           data: Data(sidecar.utf8), mimeType: "application/octet-stream")
        }
        _ = try await drive.uploadFile(name: cipherName, parentId: parentFolderId,
                                       data: ciphertext, mimeType: "application/octet-stream")
        bridge.fs.invalidateListing(cipherDir: cipherDir)
    }

    public func deleteCurrentDirectory() async throws {
        guard let folderId = bridge.driveFolderId(forVaultDir: currentPath) else {
            throw GocryptfsError.deleteFailed("Cannot resolve current folder ID")
        }
        let parent = (currentPath as NSString).deletingLastPathComponent
        let cipherParent = bridge.cipherPath(forPlainPath: parent == "." ? "" : parent, isDir: true)
        try await GoogleDriveService.shared.deleteFile(fileId: folderId)
        if let cipherParent { bridge.fs.invalidateListing(cipherDir: cipherParent) }
    }

    public func writeChunks<S: AsyncSequence>(_ chunks: S, to destinationPath: String, totalSize: Int64) async throws
        where S.Element == Data {
        // Collect all chunks into a single buffer, then encrypt and upload.
        var plainData = Data()
        plainData.reserveCapacity(Int(totalSize))
        let startTime = Date()
        var totalReceived: Int64 = 0

        for try await chunk in chunks {
            plainData.append(chunk)
            totalReceived += Int64(chunk.count)
            NotificationCenter.default.post(
                name: NSNotification.Name("com.adrianm.imagethat.fileTransferProgress"),
                object: (current: totalReceived, total: totalSize, startTime: startTime)
            )
        }

        let plainName = (destinationPath as NSString).lastPathComponent
        let plainDir = (destinationPath as NSString).deletingLastPathComponent
        let normalizedDir = (plainDir == "." || plainDir.isEmpty) ? "" : plainDir

        guard let parentFolderId = bridge.driveFolderId(forVaultDir: normalizedDir),
              let cipherDir = bridge.cipherPath(forPlainPath: normalizedDir, isDir: true),
              let cipherName = bridge.encryptFileName(cipherDirPath: cipherDir, plainName: plainName) else {
            throw GocryptfsError.writeFailed("Cannot resolve destination for \(plainName)")
        }

        let ciphertext = try bridge.encryptData(plainData)
        try await uploadEncrypted(ciphertext, cipherName: cipherName, plainName: plainName,
                                  cipherDir: cipherDir, parentFolderId: parentFolderId)

        NotificationCenter.default.post(
            name: NSNotification.Name("com.adrianm.imagethat.fileTransferProgress"),
            object: (current: totalSize, total: totalSize, startTime: startTime)
        )
    }

    // MARK: - Private

    private func readFileInChunks(path: String) throws -> Data {
        let fileSize = try bridge.fileSize(path: path)
        guard fileSize > 0 else { return Data() }

        var data = Data()
        data.reserveCapacity(Int(fileSize))
        let chunkSize: Int32 = 256 * 1024
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
            kCGImageSourceCreateThumbnailWithTransform: true,
        ]
        guard let cgImage = CGImageSourceCreateThumbnailAtIndex(source, 0, options as CFDictionary) else {
            throw MediaError.failedToLoadImage(item.name)
        }
        return NSImage(cgImage: cgImage, size: NSSize(width: cgImage.width, height: cgImage.height))
    }

    private func loadVideoThumbnail(for item: MediaItem, size: CGSize) async throws -> NSImage {
        let loader = DriveVaultResourceLoader(bridge: bridge, relativePath: item.url.path)
        var components = URLComponents()
        components.scheme = "imagethat-drivevault"
        components.host = "vault"
        components.path = "/\(item.url.path)"
        guard let url = components.url else { throw MediaError.failedToLoadVideo(item.name) }
        let asset = AVURLAsset(url: url)
        asset.resourceLoader.setDelegate(loader, queue: DispatchQueue(label: "com.imagethat.videothumb"))
        let generator = AVAssetImageGenerator(asset: asset)
        generator.maximumSize = CGSize(width: size.width * 2, height: size.height * 2)
        generator.appliesPreferredTrackTransform = true
        let cgImage = try generator.copyCGImage(at: CMTime(seconds: 1, preferredTimescale: 600), actualTime: nil)
        return NSImage(cgImage: cgImage, size: NSSize(width: cgImage.width, height: cgImage.height))
    }
}

// MARK: - DriveVaultResourceLoader

/// AVAssetResourceLoaderDelegate for streaming Drive vault video without full decryption.
final class DriveVaultResourceLoader: NSObject, AVAssetResourceLoaderDelegate, @unchecked Sendable {
    private let bridge: DriveVaultBridge
    private let relativePath: String
    private let contentType: String

    init(bridge: DriveVaultBridge, relativePath: String) {
        self.bridge = bridge
        self.relativePath = relativePath
        let ext = (relativePath as NSString).pathExtension.lowercased()
        self.contentType = UTType(filenameExtension: ext)?.identifier ?? UTType.data.identifier
        super.init()
    }

    func resourceLoader(
        _ resourceLoader: AVAssetResourceLoader,
        shouldWaitForLoadingOfRequestedResource loadingRequest: AVAssetResourceLoadingRequest
    ) -> Bool {
        if let info = loadingRequest.contentInformationRequest {
            do {
                let size = try bridge.fileSize(path: relativePath)
                info.contentLength = size
                info.contentType = contentType
                info.isByteRangeAccessSupported = true
            } catch {
                loadingRequest.finishLoading(with: error)
                return true
            }
        }

        if let dataRequest = loadingRequest.dataRequest {
            let offset = dataRequest.requestedOffset
            let length = dataRequest.requestedLength
            let toEOF = dataRequest.requestsAllDataToEndOfResource
            let bridge = self.bridge
            let path = self.relativePath
            let chunkSize: Int32 = 256 * 1024

            DispatchQueue.global(qos: .userInitiated).async {
                do {
                    var read: Int64 = 0
                    var cur = offset
                    let total: Int64 = toEOF ? .max : Int64(length)
                    while read < total {
                        if loadingRequest.isCancelled { return }
                        let remaining = total - read
                        let toRead = Int32(min(Int64(chunkSize), remaining))
                        let data = try bridge.readRange(path: path, offset: cur, length: toRead)
                        if data.isEmpty { break }
                        dataRequest.respond(with: data)
                        read += Int64(data.count)
                        cur += Int64(data.count)
                        if data.count < Int(toRead) { break }
                    }
                    loadingRequest.finishLoading()
                } catch {
                    loadingRequest.finishLoading(with: error)
                }
            }
        } else {
            loadingRequest.finishLoading()
        }
        return true
    }
}

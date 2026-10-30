import AppKit
import AVFoundation
import UniformTypeIdentifiers

// MARK: - Google Drive MediaSource

public final class GoogleDriveSource: WritableMediaSource, @unchecked Sendable {
    let folderId: String
    /// Parent folder ID, used when moving files out of this folder.
    let parentFolderId: String
    public let currentPath: String
    public let rootDisplayName = "Google Drive"

    private let service: GoogleDriveService

    // Metadata populated by listMedia() — safe to read after the await returns.
    private var fileSizes: [String: Int64] = [:]         // fileId → size
    private var thumbnailLinks: [String: String] = [:]   // fileId → thumbnailLink
    private var mimeTypes: [String: String] = [:]        // fileId → mimeType

    init(
        folderId: String,
        parentFolderId: String = "root",
        currentPath: String = "",
        service: GoogleDriveService = .shared
    ) {
        self.folderId = folderId
        self.parentFolderId = parentFolderId
        self.currentPath = currentPath
        self.service = service
    }

    // MARK: - MediaSource

    public func source(at relativePath: String) -> any MediaSource {
        // relativePath is a Drive folder ID (from SubdirectoryEntry.relativePath)
        GoogleDriveSource(folderId: relativePath, parentFolderId: folderId, currentPath: relativePath)
    }

    public func listMedia() async throws -> [MediaItem] {
        let files = try await service.listFiles(in: folderId)
        var items: [MediaItem] = []

        for file in files {
            let ext = (file.name as NSString).pathExtension.lowercased()
            guard SupportedFormats.allExtensions.contains(ext) else { continue }

            let encodedName = file.name.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? file.name
            guard let url = URL(string: "gdrive://\(file.id)/\(encodedName)") else { continue }

            let mediaType: MediaType = SupportedFormats.videoExtensions.contains(ext) ? .video : .image
            items.append(MediaItem(name: file.name, url: url, mediaType: mediaType))

            fileSizes[file.id] = file.size
            if let link = file.thumbnailLink { thumbnailLinks[file.id] = link }
            mimeTypes[file.id] = file.mimeType
        }

        return items.sorted { $0.name.localizedCaseInsensitiveCompare($1.name) == .orderedAscending }
    }

    public func listSubdirectories() async throws -> [SubdirectoryEntry] {
        let folders = try await service.listFolders(in: folderId)
        return folders.map { SubdirectoryEntry(name: $0.name, relativePath: $0.id) }
    }

    public func loadImage(for item: MediaItem) async throws -> NSImage {
        let fileId = try extractFileId(from: item)
        let data = try await service.downloadData(fileId: fileId)
        guard let image = NSImage(data: data) else { throw GoogleDriveError.invalidResponse }
        return image
    }

    public func loadThumbnail(for item: MediaItem, size: CGSize) async throws -> NSImage {
        let fileId = try extractFileId(from: item)
        let maxEdge = Int(max(size.width, size.height) * 2)

        if let link = thumbnailLinks[fileId],
           let data = try? await service.downloadThumbnail(urlString: link, maxEdge: maxEdge),
           let image = NSImage(data: data) {
            return image
        }

        return try await loadImage(for: item)
    }

    public func createPlayerItem(for item: MediaItem) throws -> AVPlayerItem {
        guard let fileId = item.url.host else { throw GoogleDriveError.invalidResponse }
        let size = fileSizes[fileId] ?? 0
        let mimeType = mimeTypes[fileId] ?? "video/mp4"
        let contentType = UTType(mimeType: mimeType)?.identifier ?? UTType.data.identifier

        var components = URLComponents()
        components.scheme = "imagethat-gdrive"
        components.host = fileId
        components.path = "/\(item.url.lastPathComponent)"
        guard let customURL = components.url else { throw GoogleDriveError.invalidResponse }

        let loader = GoogleDriveResourceLoader(service: service, fileId: fileId, fileSize: size, contentType: contentType)
        let asset = AVURLAsset(url: customURL)
        asset.resourceLoader.setDelegate(loader, queue: DispatchQueue(label: "com.imagethat.gdrive.resourceloader"))

        let playerItem = AVPlayerItem(asset: asset)
        objc_setAssociatedObject(playerItem, "gdrive-loader", loader, .OBJC_ASSOCIATION_RETAIN)
        return playerItem
    }

    public func fileSize(for item: MediaItem) async throws -> Int64 {
        let fileId = try extractFileId(from: item)
        if let known = fileSizes[fileId] { return known }
        // Not listed through this source yet: list its folder to learn it.
        _ = try await listMedia()
        guard let known = fileSizes[fileId] else { throw GoogleDriveError.notFound(item.name) }
        return known
    }

    public func readChunks(for item: MediaItem, chunkSize: Int) -> AsyncThrowingStream<Data, Error> {
        let service = self.service
        let knownSize = item.url.host.flatMap { fileSizes[$0] }
        return AsyncThrowingStream { continuation in
            let task = Task {
                do {
                    let fileId = try self.extractFileId(from: item)
                    let totalSize: Int64
                    if let knownSize {
                        totalSize = knownSize
                    } else {
                        totalSize = try await self.fileSize(for: item)
                    }
                    var offset: Int64 = 0
                    while offset < totalSize {
                        let toRead = Int(min(Int64(chunkSize), totalSize - offset))
                        let chunk = try await service.readRange(fileId: fileId, offset: offset, length: toRead)
                        if chunk.isEmpty { break }
                        continuation.yield(chunk)
                        offset += Int64(chunk.count)
                        if chunk.count < toRead { break }
                    }
                    continuation.finish()
                } catch {
                    continuation.finish(throwing: error)
                }
            }
            continuation.onTermination = { _ in task.cancel() }
        }
    }

    // MARK: - WritableMediaSource

    public func saveImage(_ imageData: Data, for item: MediaItem) async throws {
        let fileId = try extractFileId(from: item)
        let mime = mimeTypeForName(item.name)
        try await service.updateFile(fileId: fileId, data: imageData, mimeType: mime)
    }

    public func saveImageCopy(_ imageData: Data, for item: MediaItem, suffix: String) async throws -> MediaItem {
        let baseName = (item.name as NSString).deletingPathExtension
        let ext = item.fileExtension
        let newName = "\(baseName)\(suffix).\(ext)"
        let mime = mimeTypeForName(newName)

        let newFile = try await service.uploadFile(name: newName, parentId: folderId, data: imageData, mimeType: mime)

        let encodedName = newFile.name.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? newFile.name
        guard let newURL = URL(string: "gdrive://\(newFile.id)/\(encodedName)") else {
            throw GoogleDriveError.invalidResponse
        }
        return MediaItem(name: newFile.name, url: newURL, mediaType: .image)
    }

    public func deleteItem(_ item: MediaItem) async throws {
        let fileId = try extractFileId(from: item)
        try await service.deleteFile(fileId: fileId)
    }

    public func moveItem(_ item: MediaItem, to destinationPath: String) async throws {
        let fileId = try extractFileId(from: item)
        // destinationPath is the target folder ID for Drive sources
        try await service.moveFile(fileId: fileId, from: folderId, to: destinationPath)
    }

    public func createDirectory(named name: String) async throws {
        _ = try await service.createFolder(name: name, parentId: folderId)
    }

    public func deleteCurrentDirectory() async throws {
        try await service.deleteFile(fileId: folderId)
    }

    public func writeChunks<S: AsyncSequence>(_ chunks: S, to destinationPath: String, totalSize: Int64) async throws
        where S.Element == Data
    {
        var data = Data()
        data.reserveCapacity(Int(min(totalSize, Int64(Int.max))))

        let startTime = Date()
        for try await chunk in chunks {
            data.append(chunk)
            let current = Int64(data.count)
            let progressTuple = (current: current, total: totalSize, startTime: startTime)
            NotificationCenter.default.post(
                name: NSNotification.Name("com.adrianm.imagethat.fileTransferProgress"),
                object: progressTuple
            )
        }

        let fileName = (destinationPath as NSString).lastPathComponent
        _ = try await service.uploadFile(name: fileName, parentId: folderId, data: data, mimeType: mimeTypeForName(fileName))
    }

    // MARK: - Helpers

    private func extractFileId(from item: MediaItem) throws -> String {
        guard let fileId = item.url.host, !fileId.isEmpty else { throw GoogleDriveError.invalidResponse }
        return fileId
    }

    private func mimeTypeForName(_ name: String) -> String {
        let ext = (name as NSString).pathExtension.lowercased()
        switch ext {
        case "jpg", "jpeg": return "image/jpeg"
        case "png": return "image/png"
        case "heic", "heif": return "image/heic"
        case "gif": return "image/gif"
        case "webp": return "image/webp"
        case "tiff", "tif": return "image/tiff"
        case "bmp": return "image/bmp"
        case "mp4": return "video/mp4"
        case "mov": return "video/quicktime"
        case "m4v": return "video/x-m4v"
        case "avi": return "video/x-msvideo"
        case "mkv": return "video/x-matroska"
        case "webm": return "video/webm"
        default: return "application/octet-stream"
        }
    }
}

// MARK: - AVAssetResourceLoaderDelegate for Drive video streaming

final class GoogleDriveResourceLoader: NSObject, AVAssetResourceLoaderDelegate, @unchecked Sendable {
    private let service: GoogleDriveService
    private let fileId: String
    private let fileSize: Int64
    private let contentType: String

    init(service: GoogleDriveService, fileId: String, fileSize: Int64, contentType: String) {
        self.service = service
        self.fileId = fileId
        self.fileSize = fileSize
        self.contentType = contentType
        super.init()
    }

    func resourceLoader(
        _ resourceLoader: AVAssetResourceLoader,
        shouldWaitForLoadingOfRequestedResource loadingRequest: AVAssetResourceLoadingRequest
    ) -> Bool {
        if let contentRequest = loadingRequest.contentInformationRequest {
            contentRequest.contentLength = fileSize
            contentRequest.contentType = contentType
            contentRequest.isByteRangeAccessSupported = true
        }

        if let dataRequest = loadingRequest.dataRequest {
            let offset = dataRequest.requestedOffset
            let totalToRead: Int64 = dataRequest.requestsAllDataToEndOfResource
                ? fileSize - offset
                : Int64(dataRequest.requestedLength)

            let service = self.service
            let fileId = self.fileId
            let chunkSize = 256 * 1024

            Task {
                do {
                    var bytesRead: Int64 = 0
                    while bytesRead < totalToRead {
                        guard !loadingRequest.isCancelled else { return }
                        let remaining = totalToRead - bytesRead
                        let toRead = Int(min(Int64(chunkSize), remaining))
                        let chunk = try await service.readRange(
                            fileId: fileId,
                            offset: offset + bytesRead,
                            length: toRead
                        )
                        if chunk.isEmpty { break }
                        dataRequest.respond(with: chunk)
                        bytesRead += Int64(chunk.count)
                        if chunk.count < toRead { break }
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

    func resourceLoader(
        _ resourceLoader: AVAssetResourceLoader,
        didCancel loadingRequest: AVAssetResourceLoadingRequest
    ) {}
}

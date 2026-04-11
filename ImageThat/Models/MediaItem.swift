import Foundation
import UniformTypeIdentifiers

public enum MediaType: Equatable {
    case image
    case video
}

public struct MediaItem: Identifiable, Hashable {
    public let id: UUID
    public let name: String
    public let url: URL
    public let mediaType: MediaType
    public let fileExtension: String

    public init(url: URL) {
        self.id = UUID()
        self.name = url.lastPathComponent
        self.url = url
        self.fileExtension = url.pathExtension.lowercased()
        self.mediaType = Self.detectMediaType(for: url)
    }

    public init(id: UUID = UUID(), name: String, url: URL, mediaType: MediaType) {
        self.id = id
        self.name = name
        self.url = url
        self.mediaType = mediaType
        self.fileExtension = url.pathExtension.lowercased()
    }

    private static func detectMediaType(for url: URL) -> MediaType {
        let ext = url.pathExtension.lowercased()
        if SupportedFormats.videoExtensions.contains(ext) {
            return .video
        }
        return .image
    }
}

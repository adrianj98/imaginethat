import AppKit
import AVFoundation

/// Entry in a directory listing.
public struct SubdirectoryEntry: Sendable {
    public let name: String
    public let relativePath: String  // relative to source root

    public init(name: String, relativePath: String) {
        self.name = name
        self.relativePath = relativePath
    }
}

public protocol MediaSource: Sendable {
    var rootDisplayName: String { get }

    /// Relative path within this source ("" = root)
    var currentPath: String { get }

    /// Return a child source rooted at the given relative path
    func source(at relativePath: String) -> any MediaSource

    func listMedia() async throws -> [MediaItem]

    /// List subdirectories of currentPath
    func listSubdirectories() async throws -> [SubdirectoryEntry]

    func loadImage(for item: MediaItem) async throws -> NSImage
    func loadThumbnail(for item: MediaItem, size: CGSize) async throws -> NSImage

    /// Create an AVPlayerItem for the given video item (handles streaming/decryption internally)
    func createPlayerItem(for item: MediaItem) throws -> AVPlayerItem

    /// Get the plaintext size of a file
    func fileSize(for item: MediaItem) async throws -> Int64

    /// Stream file content as a sequence of Data chunks.
    func readChunks(for item: MediaItem, chunkSize: Int) -> AsyncThrowingStream<Data, Error>

    /// Default chunk size for streaming reads (256 KB)
    static var defaultChunkSize: Int { get }
}

public extension MediaSource {
    static var defaultChunkSize: Int { 262_144 }
}

/// Protocol for media sources that support writing/saving edited images.
public protocol WritableMediaSource: MediaSource {
    /// Replace the original file with edited image data.
    func saveImage(_ imageData: Data, for item: MediaItem) async throws

    /// Save a copy of the edited image alongside the original. Returns the new MediaItem.
    func saveImageCopy(_ imageData: Data, for item: MediaItem, suffix: String) async throws -> MediaItem

    /// Delete the given file
    func deleteItem(_ item: MediaItem) async throws

    /// Move an item to the destination path
    func moveItem(_ item: MediaItem, to destinationPath: String) async throws

    /// Create a directory in currentPath
    func createDirectory(named name: String) async throws

    /// Delete the current directory
    func deleteCurrentDirectory() async throws

    /// Write a sequence of Data chunks to destinationPath within this source.
    func writeChunks<S: AsyncSequence>(_ chunks: S, to destinationPath: String, totalSize: Int64) async throws
        where S.Element == Data
}

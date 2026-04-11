import AppKit
import AVFoundation

public protocol MediaSource: Sendable {
    var rootDisplayName: String { get }
    func listMedia() async throws -> [MediaItem]
    func loadImage(for item: MediaItem) async throws -> NSImage
    func loadThumbnail(for item: MediaItem, size: CGSize) async throws -> NSImage
    func videoURL(for item: MediaItem) async throws -> URL
}

import AppKit
import AVFoundation

public actor MediaLoader {
    private let source: any MediaSource

    public init(source: any MediaSource) {
        self.source = source
    }

    public func loadFullImage(for item: MediaItem) async throws -> NSImage {
        try await source.loadImage(for: item)
    }

    public func loadThumbnail(for item: MediaItem, size: CGSize) async throws -> NSImage {
        try await source.loadThumbnail(for: item, size: size)
    }
}

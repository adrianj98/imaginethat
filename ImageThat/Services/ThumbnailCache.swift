import AppKit

public final class ThumbnailCache: @unchecked Sendable {
    private let cache = NSCache<NSString, NSImage>()

    public init(countLimit: Int = 500) {
        cache.countLimit = countLimit
    }

    public func get(for id: UUID) -> NSImage? {
        cache.object(forKey: id.uuidString as NSString)
    }

    public func set(_ image: NSImage, for id: UUID) {
        cache.setObject(image, forKey: id.uuidString as NSString)
    }

    public func clear() {
        cache.removeAllObjects()
    }
}

import AppKit

extension NSImage {
    /// JPEG bytes of the image, in memory. Used to hand host-rendered previews
    /// (HEIC, RAW, video frames) to the engine's thumbnail cache.
    func jpegData(compressionFactor: Double) -> Data? {
        guard let tiff = tiffRepresentation,
              let rep = NSBitmapImageRep(data: tiff) else { return nil }
        return rep.representation(using: .jpeg, properties: [.compressionFactor: compressionFactor])
    }
}

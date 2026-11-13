import CoreGraphics

/// Zoom and pan of the photo or video in the viewer. `scale` 1 is "fit";
/// `offset` moves the zoomed media and is measured in stage points.
public struct ViewerZoom: Equatable {
    public static let minScale: CGFloat = 1
    public static let maxScale: CGFloat = 10
    /// Where a double-click zooms to.
    public static let toggleScale: CGFloat = 2.5

    public var scale: CGFloat = 1
    public var offset: CGSize = .zero

    public init() {}

    public var isZoomed: Bool { scale > Self.minScale }

    /// Multiplies the zoom, keeping `anchor` (a point measured from the stage
    /// centre) where it is on screen. The default anchor is the centre.
    public mutating func zoom(by factor: CGFloat, in stage: CGSize, anchor: CGPoint = .zero) {
        let next = max(Self.minScale, min(scale * factor, Self.maxScale))
        let ratio = next / scale
        let moved = CGSize(width: anchor.x - (anchor.x - offset.width) * ratio,
                           height: anchor.y - (anchor.y - offset.height) * ratio)
        scale = next
        offset = Self.clamp(moved, scale: next, in: stage)
    }

    public mutating func pan(by translation: CGSize, in stage: CGSize) {
        offset = offset(adding: translation, scaledBy: 1, in: stage)
    }

    /// The offset to draw during a gesture that hasn't ended: this offset plus
    /// a drag, at this scale times a pinch.
    public func offset(adding translation: CGSize, scaledBy pinch: CGFloat, in stage: CGSize) -> CGSize {
        Self.clamp(CGSize(width: offset.width + translation.width, height: offset.height + translation.height),
                   scale: max(Self.minScale, min(scale * pinch, Self.maxScale)), in: stage)
    }

    /// Double-click: zoom in on `point` (measured from the stage centre), or
    /// back to fit when already zoomed.
    public mutating func toggle(at point: CGPoint, in stage: CGSize) {
        if isZoomed {
            reset()
        } else {
            zoom(by: Self.toggleScale, in: stage, anchor: point)
        }
    }

    public mutating func reset() {
        scale = 1
        offset = .zero
    }

    /// Keeps the zoomed stage covering the stage, so the media can't be
    /// dragged out of sight.
    static func clamp(_ offset: CGSize, scale: CGFloat, in stage: CGSize) -> CGSize {
        let maxX = max(0, stage.width * (scale - 1) / 2)
        let maxY = max(0, stage.height * (scale - 1) / 2)
        return CGSize(width: max(-maxX, min(offset.width, maxX)),
                      height: max(-maxY, min(offset.height, maxY)))
    }
}

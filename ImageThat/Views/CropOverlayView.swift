import SwiftUI

/// Crop frame drawn over the fitted image. Dragging sets `pipeline.cropRect`
/// as fractions of the image, so it holds whatever size the image is shown at.
struct CropOverlayView: View {
    @ObservedObject var pipeline: ImageEditingPipeline
    /// Pixel size of the image under the overlay (rotated, uncropped).
    var imageSize: CGSize
    /// Width / height to hold the selection to; nil = free.
    var aspect: CGFloat?

    var body: some View {
        GeometryReader { geo in
            let frame = fittedRect(in: geo.size)
            let selection = pipeline.cropRect.map { r in
                CGRect(x: frame.minX + r.minX * frame.width, y: frame.minY + r.minY * frame.height,
                       width: r.width * frame.width, height: r.height * frame.height)
            } ?? frame

            ZStack(alignment: .topLeading) {
                Path { p in
                    p.addRect(frame)
                    p.addRect(selection)
                }
                .fill(Color.black.opacity(0.55), style: FillStyle(eoFill: true))

                ZStack {
                    Rectangle().stroke(Color.white, lineWidth: 1.5)
                    thirds
                }
                .frame(width: selection.width, height: selection.height)
                .overlay(alignment: .topLeading) { corner(rotation: 0) }
                .overlay(alignment: .topTrailing) { corner(rotation: 90) }
                .overlay(alignment: .bottomTrailing) { corner(rotation: 180) }
                .overlay(alignment: .bottomLeading) { corner(rotation: 270) }
                .offset(x: selection.minX, y: selection.minY)
            }
            .contentShape(Rectangle())
            .gesture(
                DragGesture(minimumDistance: 4)
                    .onChanged { value in
                        select(from: value.startLocation, to: value.location, in: frame)
                    }
            )
        }
    }

    private var thirds: some View {
        GeometryReader { g in
            Path { p in
                for i in 1...2 {
                    let x = g.size.width * CGFloat(i) / 3
                    let y = g.size.height * CGFloat(i) / 3
                    p.move(to: CGPoint(x: x, y: 0)); p.addLine(to: CGPoint(x: x, y: g.size.height))
                    p.move(to: CGPoint(x: 0, y: y)); p.addLine(to: CGPoint(x: g.size.width, y: y))
                }
            }
            .stroke(Color.white.opacity(0.45), lineWidth: 1)
        }
    }

    private func corner(rotation: Double) -> some View {
        Path { p in
            p.move(to: CGPoint(x: 0, y: 16)); p.addLine(to: .zero); p.addLine(to: CGPoint(x: 16, y: 0))
        }
        .stroke(Color.white, style: StrokeStyle(lineWidth: 3, lineCap: .round, lineJoin: .round))
        .frame(width: 16, height: 16)
        .rotationEffect(.degrees(rotation))
        .offset(x: rotation == 90 || rotation == 180 ? 3 : -3, y: rotation >= 180 ? 3 : -3)
    }

    private func fittedRect(in size: CGSize) -> CGRect {
        guard imageSize.width > 0, imageSize.height > 0 else { return CGRect(origin: .zero, size: size) }
        let scale = min(size.width / imageSize.width, size.height / imageSize.height)
        let w = imageSize.width * scale, h = imageSize.height * scale
        return CGRect(x: (size.width - w) / 2, y: (size.height - h) / 2, width: w, height: h)
    }

    private func select(from start: CGPoint, to end: CGPoint, in frame: CGRect) {
        let a = CGPoint(x: min(max(start.x, frame.minX), frame.maxX), y: min(max(start.y, frame.minY), frame.maxY))
        var w = min(max(end.x, frame.minX), frame.maxX) - a.x
        var h = min(max(end.y, frame.minY), frame.maxY) - a.y
        if let aspect, aspect > 0 {
            // Hold the ratio, then shrink both sides until it fits the image.
            h = abs(w) / aspect * (h < 0 ? -1 : 1)
            let roomX = w < 0 ? a.x - frame.minX : frame.maxX - a.x
            let roomY = h < 0 ? a.y - frame.minY : frame.maxY - a.y
            let shrink = min(1, roomX / max(abs(w), 1), roomY / max(abs(h), 1))
            w *= shrink
            h *= shrink
        }
        let rect = CGRect(x: a.x, y: a.y, width: w, height: h).standardized
        guard rect.width > 4, rect.height > 4 else { return }
        pipeline.cropRect = CGRect(x: (rect.minX - frame.minX) / frame.width,
                                   y: (rect.minY - frame.minY) / frame.height,
                                   width: rect.width / frame.width,
                                   height: rect.height / frame.height)
    }
}

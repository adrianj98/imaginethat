import Foundation
import ImageThatCore

/// The viewer's zoom and pan arithmetic: limits, panning that can't lose the
/// media off the stage, and double-click zoom around the clicked point.
enum ViewerZoomTest {
    static func run(_ check: (String, Bool, String) -> Void) {
        print("\n--- Viewer Zoom ---")
        let stage = CGSize(width: 800, height: 600)

        var z = ViewerZoom()
        check("starts at fit", z.scale == 1 && z.offset == .zero && !z.isZoomed, "\(z)")

        z.zoom(by: 1.25, in: stage)
        check("zooms in", z.scale == 1.25 && z.isZoomed, "\(z.scale)")

        z.zoom(by: 100, in: stage)
        check("stops at the maximum", z.scale == ViewerZoom.maxScale, "\(z.scale)")

        z.zoom(by: 0.0001, in: stage)
        check("stops at fit and recentres", z.scale == 1 && z.offset == .zero, "\(z)")

        z.pan(by: CGSize(width: 50, height: 50), in: stage)
        check("does not pan at fit", z.offset == .zero, "\(z.offset)")

        z = ViewerZoom()
        z.zoom(by: 2, in: stage)
        z.pan(by: CGSize(width: 100, height: -50), in: stage)
        check("pans while zoomed", z.offset == CGSize(width: 100, height: -50), "\(z.offset)")

        z.pan(by: CGSize(width: 5000, height: -5000), in: stage)
        check("pan stops at the edge", z.offset == CGSize(width: 400, height: -300), "\(z.offset)")

        z.zoom(by: 0.75, in: stage)
        check("zooming out pulls the pan back inside",
              z.scale == 1.5 && z.offset == CGSize(width: 200, height: -150), "\(z)")

        // Double-click: the clicked point stays under the cursor.
        z = ViewerZoom()
        let click = CGPoint(x: 100, y: -60) // measured from the stage centre
        z.toggle(at: click, in: stage)
        check("double-click zooms in", z.scale == ViewerZoom.toggleScale, "\(z.scale)")
        check("double-click keeps the clicked point in place",
              abs(z.offset.width - (100 - 100 * 2.5)) < 0.001 && abs(z.offset.height - (-60 + 60 * 2.5)) < 0.001,
              "\(z.offset)")

        z.toggle(at: click, in: stage)
        check("second double-click returns to fit", z.scale == 1 && z.offset == .zero, "\(z)")

        z = ViewerZoom()
        z.toggle(at: CGPoint(x: 400, y: 300), in: stage)
        check("double-click in a corner stays inside the stage",
              z.offset == CGSize(width: -600, height: -450), "\(z.offset)")

        z.reset()
        check("reset returns to fit", z == ViewerZoom(), "\(z)")

        z.zoom(by: 2, in: stage)
        let live = z.offset(adding: CGSize(width: 9999, height: 0), scaledBy: 1, in: stage)
        check("a live drag is clamped without being committed",
              live == CGSize(width: 400, height: 0) && z.offset == .zero, "\(live)")
    }
}

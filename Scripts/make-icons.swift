// Generates every app icon from the SVG artwork below, so the Mac, Android
// and web apps share one design. Run from the repo root:
//
//   swift Scripts/make-icons.swift
//
// Writes:
//   ImageThat/Resources/Assets.xcassets/AppIcon.appiconset/*.png (Xcode builds)
//   ImageThat/Resources/AppIcon.icns                             (make app)
//   android/app/src/main/res/mipmap-*/ic_launcher*.png           (adaptive icon layers)
//   webserver/web/public/favicon.svg, apple-touch-icon.png
//
// NSImage renders SVG natively (CoreSVG), so this needs nothing beyond Xcode.
import AppKit

// MARK: Artwork (1024 x 1024 design grid)

/// The symbol: a gold-rimmed keyhole, and through it a sunset over mountains
/// and a lake: the pictures, seen through the lock that keeps them. One path
/// draws the rim and clips the scene. Its bounding box is about
/// (316, 178)-(708, 886), centred on (512, 532).
let keyhole = "M412 573 A200 200 0 1 1 612 573 L686 846 Q512 884 338 846 Z"

let glyph = """
<path d="\(keyhole)" fill="none" stroke="url(#rim)" stroke-width="44" stroke-linejoin="round"/>
<clipPath id="hole"><path d="\(keyhole)"/></clipPath>
<g clip-path="url(#hole)">
  <rect x="300" y="190" width="424" height="720" fill="url(#sky)"/>
  <circle cx="512" cy="500" r="92" fill="url(#sun)"/>
  <path d="M300 640 L300 560 L400 470 L470 530 L560 440 L724 580 L724 640 Z" fill="#6b2d8f"/>
  <rect x="300" y="600" width="424" height="300" fill="url(#lake)"/>
  <rect x="452" y="620" width="120" height="10" rx="5" fill="#fff3c4" opacity="0.8"/>
  <rect x="472" y="650" width="80" height="8" rx="4" fill="#fff3c4" opacity="0.6"/>
  <rect x="490" y="676" width="44" height="7" rx="3.5" fill="#fff3c4" opacity="0.45"/>
  <path d="M300 900 L300 760 L420 700 L540 790 L724 720 L724 900 Z" fill="#1d0f3a"/>
  <circle cx="440" cy="290" r="4" fill="#ffffff" opacity="0.85"/>
  <circle cx="590" cy="250" r="3" fill="#ffffff" opacity="0.85"/>
  <circle cx="530" cy="330" r="2.5" fill="#ffffff" opacity="0.7"/>
</g>
"""

/// A few stars on the night sky behind the keyhole.
let stars = """
<g fill="#ffffff" opacity="0.55">
  <circle cx="230" cy="250" r="5"/><circle cx="800" cy="210" r="4"/><circle cx="800" cy="440" r="6"/>
  <circle cx="215" cy="560" r="4"/><circle cx="300" cy="170" r="3"/><circle cx="760" cy="760" r="4"/>
</g>
"""

let defs = """
<defs>
  <linearGradient id="bg" x1="0" y1="0" x2="0" y2="1">
    <stop offset="0" stop-color="#1c2340"/>
    <stop offset="1" stop-color="#07091a"/>
  </linearGradient>
  <linearGradient id="sky" x1="0" y1="0" x2="0" y2="1">
    <stop offset="0" stop-color="#2a1a7a"/>
    <stop offset="0.5" stop-color="#d83f7d"/>
    <stop offset="0.75" stop-color="#ffae4a"/>
  </linearGradient>
  <radialGradient id="sun" cx="0.5" cy="0.5" r="0.5">
    <stop offset="0" stop-color="#fff8d6"/>
    <stop offset="0.75" stop-color="#ffd166"/>
    <stop offset="1" stop-color="#ffb347"/>
  </radialGradient>
  <linearGradient id="rim" x1="0" y1="0" x2="0" y2="1">
    <stop offset="0" stop-color="#ffe9a8"/>
    <stop offset="1" stop-color="#c8862b"/>
  </linearGradient>
  <linearGradient id="lake" x1="0" y1="0" x2="0" y2="1">
    <stop offset="0" stop-color="#ff9a5a"/>
    <stop offset="1" stop-color="#3a1a6a"/>
  </linearGradient>
</defs>
"""

func svg(_ body: String) -> String {
    """
    <svg xmlns="http://www.w3.org/2000/svg" width="1024" height="1024" viewBox="0 0 1024 1024">
    \(defs)
    \(body)
    </svg>
    """
}

/// macOS: the rounded body sits inside Apple's 824 pt grid with room for a shadow.
let macSVG = svg("""
<rect x="100" y="100" width="824" height="824" rx="185" fill="url(#bg)"/>
\(stars)
<g transform="translate(512 512) scale(0.98) translate(-512 -532)">\(glyph)</g>
""")

/// Web favicon: full bleed, the glyph a little larger so it reads at 16 px.
let faviconSVG = svg("""
<rect width="1024" height="1024" rx="228" fill="url(#bg)"/>
<g transform="translate(512 512) scale(1.3) translate(-512 -532)">\(glyph)</g>
""")

/// iOS home screen (apple-touch-icon): square; iOS rounds the corners itself.
let touchSVG = svg("""
<rect width="1024" height="1024" fill="url(#bg)"/>
\(stars)
<g transform="translate(512 512) scale(1.12) translate(-512 -532)">\(glyph)</g>
""")

/// Android adaptive icon layers (108 dp canvas; launchers may crop to the
/// central 66 dp circle, so the glyph is kept inside it).
let androidBackgroundSVG = svg(#"<rect width="1024" height="1024" fill="url(#bg)"/>\#(stars)"#)
let androidForegroundSVG = svg(#"<g transform="translate(512 512) scale(0.76) translate(-512 -532)">\#(glyph)</g>"#)

// MARK: Rendering

let root = URL(fileURLWithPath: FileManager.default.currentDirectoryPath)

func render(_ svgText: String, pixels: Int, shadow: Bool = false) -> Data {
    guard let image = NSImage(data: Data(svgText.utf8)) else { fatalError("SVG did not parse") }
    let rep = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: pixels, pixelsHigh: pixels,
                               bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
                               colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)!
    NSGraphicsContext.saveGraphicsState()
    NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: rep)
    NSGraphicsContext.current?.imageInterpolation = .high
    if shadow {
        // CoreSVG ignores SVG filters, so the drop shadow is drawn here.
        let s = CGFloat(pixels) / 1024
        let ns = NSShadow()
        ns.shadowColor = NSColor.black.withAlphaComponent(0.32)
        ns.shadowBlurRadius = 26 * s
        ns.shadowOffset = NSSize(width: 0, height: -12 * s)
        ns.set()
    }
    image.draw(in: NSRect(x: 0, y: 0, width: pixels, height: pixels))
    NSGraphicsContext.restoreGraphicsState()
    return rep.representation(using: .png, properties: [:])!
}

func write(_ data: Data, _ path: String) {
    let url = root.appendingPathComponent(path)
    try! FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
    try! data.write(to: url)
    print("wrote \(path)")
}

// macOS asset catalog and .icns (built with iconutil from a temporary .iconset).
let appiconset = "ImageThat/Resources/Assets.xcassets/AppIcon.appiconset"
let iconset = FileManager.default.temporaryDirectory.appendingPathComponent("AppIcon.iconset")
try? FileManager.default.removeItem(at: iconset)
try! FileManager.default.createDirectory(at: iconset, withIntermediateDirectories: true)
var catalog: [[String: String]] = []
for size in [16, 32, 128, 256, 512] {
    for scale in [1, 2] {
        let name = scale == 1 ? "icon_\(size)x\(size).png" : "icon_\(size)x\(size)@2x.png"
        let png = render(macSVG, pixels: size * scale, shadow: true)
        write(png, "\(appiconset)/\(name)")
        try! png.write(to: iconset.appendingPathComponent(name))
        catalog.append(["idiom": "mac", "scale": "\(scale)x", "size": "\(size)x\(size)", "filename": name])
    }
}
let contents: [String: Any] = ["images": catalog, "info": ["author": "xcode", "version": 1]]
write(try! JSONSerialization.data(withJSONObject: contents, options: [.prettyPrinted, .sortedKeys]),
      "\(appiconset)/Contents.json")

let icns = root.appendingPathComponent("ImageThat/Resources/AppIcon.icns")
let iconutil = Process()
iconutil.executableURL = URL(fileURLWithPath: "/usr/bin/iconutil")
iconutil.arguments = ["-c", "icns", iconset.path, "-o", icns.path]
try! iconutil.run()
iconutil.waitUntilExit()
precondition(iconutil.terminationStatus == 0, "iconutil failed")
print("wrote ImageThat/Resources/AppIcon.icns")

// Android: adaptive icon layers at each density (108 dp). minSdk is 29, so no legacy icon is needed.
let densities: [(String, Double)] = [("mdpi", 1), ("hdpi", 1.5), ("xhdpi", 2), ("xxhdpi", 3), ("xxxhdpi", 4)]
for (density, scale) in densities {
    let px = Int(108 * scale)
    let dir = "android/app/src/main/res/mipmap-\(density)"
    write(render(androidForegroundSVG, pixels: px), "\(dir)/ic_launcher_foreground.png")
    write(render(androidBackgroundSVG, pixels: px), "\(dir)/ic_launcher_background.png")
}

// Web.
write(Data(faviconSVG.utf8), "webserver/web/public/favicon.svg")
write(render(touchSVG, pixels: 180), "webserver/web/public/apple-touch-icon.png")

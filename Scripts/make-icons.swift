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

/// The symbol: a photo card over a second card, with a padlock badge.
/// Its bounding box is about (240, 270)-(838, 778).
let glyph = """
<g transform="rotate(-8 512 492)">
  <rect x="262" y="292" width="500" height="400" rx="56" fill="#ffffff" fill-opacity="0.4"/>
</g>
<rect x="262" y="292" width="500" height="400" rx="56" fill="#ffffff"/>
<clipPath id="pic"><rect x="298" y="328" width="428" height="328" rx="28"/></clipPath>
<g clip-path="url(#pic)">
  <rect x="298" y="328" width="428" height="328" fill="url(#sky)"/>
  <circle cx="626" cy="414" r="40" fill="#ffc83d"/>
  <path d="M298 656 L298 590 L410 470 L520 590 L570 540 L726 656 Z" fill="#3f8cff"/>
  <path d="M298 656 L298 640 L392 560 L470 620 L560 548 L726 656 Z" fill="#0a5bd8"/>
</g>
<circle cx="720" cy="660" r="118" fill="#0b1b3a" stroke="#ffffff" stroke-width="20"/>
<path d="M690 652 V626 a30 30 0 0 1 60 0 V652" fill="none" stroke="#ffffff" stroke-width="17" stroke-linecap="round"/>
<rect x="668" y="646" width="104" height="80" rx="16" fill="#ffffff"/>
<circle cx="720" cy="680" r="11" fill="#0b1b3a"/>
<rect x="714" y="684" width="12" height="22" rx="5" fill="#0b1b3a"/>
"""

let defs = """
<defs>
  <linearGradient id="bg" x1="0" y1="0" x2="0" y2="1">
    <stop offset="0" stop-color="#4fa6ff"/>
    <stop offset="0.55" stop-color="#0a74ff"/>
    <stop offset="1" stop-color="#0052d6"/>
  </linearGradient>
  <linearGradient id="sky" x1="0" y1="0" x2="0" y2="1">
    <stop offset="0" stop-color="#e3efff"/>
    <stop offset="1" stop-color="#bcd7ff"/>
  </linearGradient>
  <linearGradient id="shine" x1="0" y1="0" x2="0" y2="1">
    <stop offset="0" stop-color="#ffffff" stop-opacity="0.22"/>
    <stop offset="0.5" stop-color="#ffffff" stop-opacity="0"/>
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
<rect x="100" y="100" width="824" height="824" rx="185" fill="url(#shine)"/>
<g transform="translate(512 512) scale(0.98) translate(-539 -524)">\(glyph)</g>
""")

/// Web favicon: full bleed, the glyph a little larger so it reads at 16 px.
let faviconSVG = svg("""
<rect width="1024" height="1024" rx="228" fill="url(#bg)"/>
<g transform="translate(512 512) scale(1.18) translate(-539 -524)">\(glyph)</g>
""")

/// iOS home screen (apple-touch-icon): square; iOS rounds the corners itself.
let touchSVG = svg("""
<rect width="1024" height="1024" fill="url(#bg)"/>
<g transform="translate(512 512) scale(1.12) translate(-539 -524)">\(glyph)</g>
""")

/// Android adaptive icon layers (108 dp canvas; launchers may crop to the
/// central 66 dp circle, so the glyph is kept inside it).
let androidBackgroundSVG = svg(#"<rect width="1024" height="1024" fill="url(#bg)"/>"#)
let androidForegroundSVG = svg(#"<g transform="translate(512 512) scale(0.78) translate(-539 -524)">\#(glyph)</g>"#)

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

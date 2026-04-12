import AppKit
import CoreImage
import CoreGraphics
import Combine

final class ImageEditingPipeline: NSObject, ObservableObject {
    @Published var brightness: Float = 0      // -1…1
    @Published var contrast: Float = 1        // 0…2
    @Published var saturation: Float = 1      // 0…2
    @Published var sharpness: Float = 0       // 0…1
    @Published var rotation: Int = 0          // 0, 90, 180, 270
    @Published var cropRect: CGRect? = nil    // nil = no crop

    var isModified: Bool {
        brightness != 0 || contrast != 1 || saturation != 1 || sharpness != 0 || rotation != 0 || cropRect != nil
    }

    /// Renders the edited image from a source CGImage.
    func render(source: CGImage) -> NSImage {
        var ciImage = CIImage(cgImage: source)

        // Apply color adjustments
        if brightness != 0 || contrast != 1 || saturation != 1 {
            let colorControls = CIFilter(name: "CIColorControls")!
            colorControls.setValue(ciImage, forKey: kCIInputImageKey)
            colorControls.setValue(brightness, forKey: "inputBrightness")
            colorControls.setValue(contrast, forKey: "inputContrast")
            colorControls.setValue(saturation, forKey: "inputSaturation")
            ciImage = colorControls.outputImage!
        }

        // Apply sharpness
        if sharpness != 0 {
            let sharpen = CIFilter(name: "CISharpenLuminance")!
            sharpen.setValue(ciImage, forKey: kCIInputImageKey)
            sharpen.setValue(sharpness, forKey: kCIInputSharpnessKey)
            ciImage = sharpen.outputImage!
        }

        // Apply rotation (convert 90-degree increments to radians)
        if rotation != 0 {
            let radians = CGFloat(rotation) * .pi / 180
            let transform = CGAffineTransform(rotationAngle: radians)
            ciImage = ciImage.transformed(by: transform)
        }

        // Apply crop
        if let cropRect = cropRect {
            ciImage = ciImage.cropped(to: cropRect)
        }

        // Render back to CGImage
        let context = CIContext()
        let cgImageResult = context.createCGImage(ciImage, from: ciImage.extent)!

        return NSImage(cgImage: cgImageResult, size: NSZeroSize)
    }

    /// Export rendered image as PNG or JPEG bytes.
    func exportData(source: CGImage, format: String = "jpeg") -> Data {
        let edited = render(source: source)

        guard let tiffData = edited.tiffRepresentation,
              let bitmap = NSBitmapImageRep(data: tiffData) else {
            return Data()
        }

        if format.lowercased() == "png" {
            return bitmap.representation(using: .png, properties: [:]) ?? Data()
        } else {
            return bitmap.representation(using: .jpeg, properties: [.compressionFactor: 0.92]) ?? Data()
        }
    }

    /// Reset all edits to defaults.
    func reset() {
        brightness = 0
        contrast = 1
        saturation = 1
        sharpness = 0
        rotation = 0
        cropRect = nil
    }
}

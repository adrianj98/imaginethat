import SwiftUI

enum EditMode: String, CaseIterable {
    case adjust = "Adjust"
    case crop = "Crop"
}

/// Crop aspect presets; `ratio(for:)` resolves "Original" from the image.
enum CropAspect: String, CaseIterable {
    case free = "Free"
    case original = "Original"
    case fourThree = "4 : 3"
    case sixteenNine = "16 : 9"
    case square = "Square"

    func ratio(for size: CGSize) -> CGFloat? {
        switch self {
        case .free: return nil
        case .original: return size.height > 0 ? size.width / size.height : nil
        case .fourThree: return 4.0 / 3.0
        case .sixteenNine: return 16.0 / 9.0
        case .square: return 1
        }
    }
}

/// The floating controls card shown in place of Info while editing.
struct EditPanelView: View {
    @ObservedObject var pipeline: ImageEditingPipeline
    let mode: EditMode
    @Binding var aspect: CropAspect
    let fileName: String
    let isSourceWritable: Bool

    var body: some View {
        VStack(alignment: .leading, spacing: 22) {
            switch mode {
            case .adjust: adjustSection
            case .crop: cropSection
            }

            Spacer(minLength: 0)

            VStack(alignment: .leading, spacing: 12) {
                Button("Reset All") { pipeline.reset() }
                    .buttonStyle(PillButtonStyle(dark: true, height: 36))
                    .frame(maxWidth: .infinity)
                    .disabled(!pipeline.isModified)
                footnote
                    .font(.system(size: 11))
                    .foregroundStyle(Glass.label)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
        .padding(18)
        .frame(maxHeight: .infinity, alignment: .top)
        .glassCard(dark: true)
    }

    private var footnote: Text {
        let base = (fileName as NSString).deletingPathExtension
        let ext = (fileName as NSString).pathExtension
        let copyName = ext.isEmpty ? "\(base)-edited" : "\(base)-edited.\(ext)"
        let copy = Text("Save Copy").bold().foregroundColor(.white) + Text(" adds \(copyName) beside it.")
        guard isSourceWritable else { return copy }
        return Text("Replace").bold().foregroundColor(.white) + Text(" writes over the original. ") + copy
    }

    private var adjustSection: some View {
        VStack(alignment: .leading, spacing: 16) {
            SectionHeading(title: "Light & color")
            slider("Brightness", value: $pipeline.brightness, range: -1...1, display: { $0 * 100 })
            slider("Contrast", value: $pipeline.contrast, range: 0...2, display: { ($0 - 1) * 100 })
            slider("Saturation", value: $pipeline.saturation, range: 0...2, display: { ($0 - 1) * 100 })
            slider("Sharpness", value: $pipeline.sharpness, range: 0...1, display: { $0 * 100 })
        }
    }

    private func slider(_ title: String, value: Binding<Float>, range: ClosedRange<Float>,
                        display: (Float) -> Float) -> some View {
        let shown = Int(display(value.wrappedValue).rounded())
        return VStack(alignment: .leading, spacing: 8) {
            HStack {
                Text(title)
                Spacer()
                Text(shown > 0 ? "+\(shown)" : "\(shown)")
                    .monospacedDigit()
                    .foregroundStyle(.secondary)
            }
            .font(.system(size: 13))
            Slider(value: value, in: range)
                .tint(.white)
                .controlSize(.small)
                .accessibilityLabel(title)
        }
    }

    private var cropSection: some View {
        VStack(alignment: .leading, spacing: 22) {
            VStack(alignment: .leading, spacing: 10) {
                SectionHeading(title: "Rotate")
                HStack(spacing: 8) {
                    Button { rotate(by: -90) } label: {
                        Label("Left", systemImage: "rotate.left").frame(maxWidth: .infinity)
                    }
                    Button { rotate(by: 90) } label: {
                        Label("Right", systemImage: "rotate.right").frame(maxWidth: .infinity)
                    }
                }
                .buttonStyle(PillButtonStyle(dark: true, height: 36))
            }
            VStack(alignment: .leading, spacing: 10) {
                SectionHeading(title: "Aspect")
                FlowLayout(spacing: 6) {
                    ForEach(CropAspect.allCases, id: \.self) { option in
                        let on = option == aspect
                        Button(option.rawValue) {
                            aspect = option
                            pipeline.cropRect = nil
                        }
                        .buttonStyle(.plain)
                        .font(.system(size: 12, weight: on ? .semibold : .regular))
                        .padding(.horizontal, 12)
                        .frame(height: 30)
                        .foregroundStyle(on ? Glass.darkCanvas : Color.white.opacity(0.85))
                        .background(Capsule().fill(on ? Color.white : Color.white.opacity(0.1)))
                        .accessibilityAddTraits(on ? .isSelected : [])
                    }
                }
            }
        }
    }

    private func rotate(by degrees: Int) {
        // A crop is a fraction of the rotated frame, so it no longer applies.
        pipeline.cropRect = nil
        pipeline.rotation = (pipeline.rotation + degrees + 360) % 360
    }
}

import SwiftUI

/// Shared look from the Mac redesign: floating translucent capsules and cards
/// over edge-to-edge content.
enum Glass {
    static let lightCanvas = Color(red: 0xF4 / 255, green: 0xF4 / 255, blue: 0xF7 / 255)
    static let darkCanvas = Color(red: 0x14 / 255, green: 0x14 / 255, blue: 0x16 / 255)
    static let label = Color(red: 0x8E / 255, green: 0x8E / 255, blue: 0x93 / 255)
    static let edited = Color(red: 0xFF / 255, green: 0xB3 / 255, blue: 0x40 / 255)
}

private struct GlassBackground<S: InsettableShape>: ViewModifier {
    let shape: S
    let dark: Bool

    func body(content: Content) -> some View {
        content
            .background(dark ? AnyShapeStyle(Color(white: 0.16).opacity(0.6)) : AnyShapeStyle(.regularMaterial), in: shape)
            .background(.ultraThinMaterial, in: shape)
            .overlay(shape.strokeBorder(dark ? Color.white.opacity(0.14) : Color.black.opacity(0.1), lineWidth: 0.5))
            .shadow(color: .black.opacity(dark ? 0.3 : 0.08), radius: 10, y: 5)
    }
}

extension View {
    /// A floating capsule, 40pt tall, used for every toolbar group.
    func glassCapsule(dark: Bool = false) -> some View {
        frame(height: 40)
            .modifier(GlassBackground(shape: Capsule(), dark: dark))
    }

    /// A floating card (sidebar-style info and edit panels).
    func glassCard(dark: Bool = false, radius: CGFloat = 16) -> some View {
        modifier(GlassBackground(shape: RoundedRectangle(cornerRadius: radius, style: .continuous), dark: dark))
    }
}

/// A round 32pt icon button for inside a toolbar capsule.
struct GlassIconButton: View {
    let systemImage: String
    let help: String
    var isOn = false
    var dark = false
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            Image(systemName: systemImage)
                .font(.system(size: 14, weight: .medium))
                .frame(width: 32, height: 32)
                .foregroundStyle(isOn ? (dark ? Color.white : Color.accentColor) : (dark ? Color.white : Color.primary))
                .background(Circle().fill(isOn ? (dark ? Color.white.opacity(0.16) : Color.accentColor.opacity(0.14)) : .clear))
                .contentShape(Circle())
        }
        .buttonStyle(.plain)
        .help(help)
        .accessibilityLabel(help)
    }
}

/// Capsule push button: `.prominent` is the filled accent action.
struct PillButtonStyle: ButtonStyle {
    enum Kind { case prominent, plain, quiet }
    var kind: Kind = .plain
    var dark = false
    var height: CGFloat = 32

    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.system(size: 13, weight: kind == .prominent ? .semibold : .regular))
            .padding(.horizontal, 14)
            .frame(height: height)
            .foregroundStyle(kind == .prominent ? Color.white : (dark ? Color.white : Color.primary))
            .background(Capsule().fill(fill))
            .opacity(configuration.isPressed ? 0.75 : 1)
            .contentShape(Capsule())
    }

    private var fill: Color {
        switch kind {
        case .prominent: return .accentColor
        case .plain: return dark ? Color.white.opacity(0.1) : Color.black.opacity(0.06)
        case .quiet: return .clear
        }
    }
}

/// Small grey section heading used in cards and settings.
struct SectionHeading: View {
    let title: String
    var body: some View {
        Text(title)
            .font(.system(size: 12, weight: .semibold))
            .foregroundStyle(Glass.label)
    }
}

/// Wrapping row of tag chips.
struct TagChips: View {
    let tags: [String]
    var dark = false

    var body: some View {
        FlowLayout(spacing: 6) {
            ForEach(tags, id: \.self) { tag in
                Text(tag)
                    .font(.system(size: 12, weight: .medium))
                    .padding(.horizontal, 10)
                    .padding(.vertical, 4)
                    .foregroundStyle(dark ? Color.white : Color.accentColor)
                    .background(Capsule().fill(dark ? Color.white.opacity(0.12) : Color.accentColor.opacity(0.14)))
            }
        }
    }
}

struct FlowLayout: Layout {
    var spacing: CGFloat = 6

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let rows = arrange(width: proposal.width ?? .infinity, subviews: subviews)
        let height = rows.last.map { $0.y + $0.height } ?? 0
        return CGSize(width: proposal.width ?? rows.map(\.width).max() ?? 0, height: height)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        let rows = arrange(width: bounds.width, subviews: subviews)
        for row in rows {
            var x = bounds.minX
            for index in row.indices {
                let size = subviews[index].sizeThatFits(.unspecified)
                subviews[index].place(at: CGPoint(x: x, y: bounds.minY + row.y), proposal: ProposedViewSize(size))
                x += size.width + spacing
            }
        }
    }

    private struct Row { var indices: [Int] = []; var y: CGFloat = 0; var width: CGFloat = 0; var height: CGFloat = 0 }

    private func arrange(width: CGFloat, subviews: Subviews) -> [Row] {
        var rows: [Row] = [Row()]
        var y: CGFloat = 0
        for index in subviews.indices {
            let size = subviews[index].sizeThatFits(.unspecified)
            if !rows[rows.count - 1].indices.isEmpty && rows[rows.count - 1].width + spacing + size.width > width {
                y += rows[rows.count - 1].height + spacing
                rows.append(Row(y: y))
            }
            var row = rows[rows.count - 1]
            row.width += (row.indices.isEmpty ? 0 : spacing) + size.width
            row.height = max(row.height, size.height)
            row.indices.append(index)
            rows[rows.count - 1] = row
        }
        return rows
    }
}

/// "Type · W × H · size" line and created date for an item, read lazily.
struct ItemFacts {
    var kind = ""
    var dimensions: CGSize?
    var bytes: Int64?
    var created: Date?

    var summary: String {
        var parts = [kind]
        if let d = dimensions { parts.append("\(Int(d.width)) × \(Int(d.height))") }
        if let b = bytes { parts.append(ByteCountFormatter.string(fromByteCount: b, countStyle: .file)) }
        return parts.filter { !$0.isEmpty }.joined(separator: " · ")
    }

    static func kind(for item: MediaItem) -> String {
        switch item.fileExtension {
        case "mov": return "QuickTime"
        case "mp4", "m4v": return "MPEG-4"
        case "jpg", "jpeg": return "JPEG"
        case "": return item.mediaType == .video ? "Video" : "Image"
        default: return item.fileExtension.uppercased()
        }
    }
}

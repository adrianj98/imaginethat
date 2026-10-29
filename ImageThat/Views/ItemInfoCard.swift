import SwiftUI

/// The floating Info card: name, type/size line, dates, and for images the
/// on-device auto-tags and recognized text. Everything it decodes stays in
/// memory.
struct ItemInfoCard: View {
    @EnvironmentObject var appState: AppState
    let item: MediaItem
    var dark = false
    /// Thumbnail shown at the top (grid only).
    var preview: NSImage?
    /// Decoded image the viewer already holds.
    var image: CGImage?
    /// False when `image` will arrive from the caller, so the card doesn't decode a second copy.
    var loadsImage = true
    var location: String?
    var footnote: String?

    @State private var facts = ItemFacts()
    @State private var tags: [String] = []
    @State private var text = ""
    @State private var isAnalyzing = false
    @State private var visionService = VisionAnalysisService()

    var body: some View {
        VStack(alignment: .leading, spacing: 18) {
            if let preview {
                Image(nsImage: preview)
                    .resizable()
                    .aspectRatio(contentMode: .fill)
                    .frame(maxWidth: .infinity)
                    .frame(height: 150)
                    .clipShape(RoundedRectangle(cornerRadius: 12, style: .continuous))
            }

            VStack(alignment: .leading, spacing: 3) {
                Text(item.name)
                    .font(.system(size: 15, weight: .semibold))
                    .lineLimit(1)
                    .truncationMode(.middle)
                Text(facts.summary)
                    .font(.system(size: 12))
                    .foregroundStyle(.secondary)
            }

            Grid(alignment: .leadingFirstTextBaseline, horizontalSpacing: 10, verticalSpacing: 8) {
                if let created = facts.created {
                    factRow("Created", created.formatted(date: .abbreviated, time: .shortened))
                }
                if let location {
                    factRow("Location", location)
                }
            }
            .font(.system(size: 12))

            if item.mediaType == .image {
                ScrollView {
                    VStack(alignment: .leading, spacing: 18) {
                        tagsSection
                        textSection
                    }
                }
            }

            Spacer(minLength: 0)

            if let caching = appState.mediaSource as? OfflineCaching, caching.canCache {
                CacheFileRow(item: item, source: caching, dark: dark)
            }

            if let footnote {
                Label(footnote, systemImage: "lock")
                    .font(.system(size: 11))
                    .foregroundStyle(Glass.label)
            }
        }
        .padding(dark ? 18 : 16)
        .frame(maxHeight: .infinity, alignment: .top)
        .glassCard(dark: dark)
        .task(id: "\(item.id)-\(image != nil)") { await load() }
    }

    private func factRow(_ label: String, _ value: String) -> some View {
        GridRow {
            Text(label).foregroundStyle(Glass.label).frame(width: 66, alignment: .leading)
            Text(value).lineLimit(2)
        }
    }

    private var tagsSection: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                SectionHeading(title: "Auto-Tags")
                Spacer()
                Text("On-device").font(.system(size: 11)).foregroundStyle(.tertiary)
            }
            if isAnalyzing && tags.isEmpty {
                analyzing
            } else if tags.isEmpty {
                Text("No tags found").font(.system(size: 12)).foregroundStyle(.tertiary)
            } else {
                TagChips(tags: tags, dark: dark)
            }
        }
    }

    private var textSection: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                SectionHeading(title: "Recognized Text")
                Spacer()
                if !text.isEmpty {
                    Button("Copy") {
                        NSPasteboard.general.clearContents()
                        NSPasteboard.general.setString(text, forType: .string)
                    }
                    .buttonStyle(PillButtonStyle(dark: dark, height: 24))
                    .font(.system(size: 12))
                }
            }
            if isAnalyzing && text.isEmpty {
                analyzing
            } else if text.isEmpty {
                Text("No text found").font(.system(size: 12)).foregroundStyle(.tertiary)
            } else {
                Text(text)
                    .font(.system(size: 12))
                    .lineSpacing(3)
                    .textSelection(.enabled)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 10)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(RoundedRectangle(cornerRadius: 10).fill(dark ? Color.white.opacity(0.06) : Color.black.opacity(0.04)))
            }
        }
    }

    private var analyzing: some View {
        HStack(spacing: 8) {
            ProgressView().controlSize(.small)
            Text("Analyzing…").font(.system(size: 12)).foregroundStyle(.secondary)
        }
    }

    private func load() async {
        facts = ItemFacts(kind: ItemFacts.kind(for: item))
        tags = []
        text = ""
        guard let source = appState.mediaSource else { return }
        if item.url.isFileURL,
           let attrs = try? FileManager.default.attributesOfItem(atPath: item.url.path) {
            facts.created = attrs[.creationDate] as? Date
        }
        facts.bytes = try? await source.fileSize(for: item)
        guard item.mediaType == .image else { return }

        var cg = image
        if cg == nil, loadsImage, let loaded = try? await source.loadImage(for: item) {
            cg = loaded.cgImage(forProposedRect: nil, context: nil, hints: nil)
        }
        guard let cg, !Task.isCancelled else { return }
        facts.dimensions = CGSize(width: cg.width, height: cg.height)

        isAnalyzing = true
        async let foundTags = visionService.classifyImage(cg)
        async let foundText = visionService.recognizeText(in: cg)
        let result = (try? await foundTags, try? await foundText)
        if !Task.isCancelled {
            tags = result.0 ?? []
            text = result.1 ?? ""
        }
        isAnalyzing = false
    }
}

/// Keep-on-this-Mac control for remote and Drive vault files: a button, the
/// fetch's progress with Cancel, or the cached state with Remove.
private struct CacheFileRow: View {
    @EnvironmentObject var appState: AppState
    let item: MediaItem
    let source: any OfflineCaching
    let dark: Bool

    @State private var isCached = false

    var body: some View {
        Group {
            if let progress = appState.cacheProgress[item.url] {
                VStack(alignment: .leading, spacing: 6) {
                    HStack {
                        Text("Caching… \(Int(progress * 100))%")
                            .font(.system(size: 12))
                            .foregroundStyle(.secondary)
                        Spacer()
                        Button("Cancel") { appState.cancelCaching(item) }
                            .buttonStyle(PillButtonStyle(kind: .quiet, dark: dark, height: 24))
                            .font(.system(size: 12))
                    }
                    ProgressView(value: progress)
                        .progressViewStyle(.linear)
                        .controlSize(.small)
                }
            } else if isCached {
                HStack {
                    Label("Cached on this Mac", systemImage: "checkmark.circle.fill")
                        .font(.system(size: 12))
                        .foregroundStyle(.secondary)
                    Spacer()
                    Button("Remove") { appState.uncacheFile(item) }
                        .buttonStyle(PillButtonStyle(kind: .quiet, dark: dark, height: 24))
                        .font(.system(size: 12))
                }
            } else {
                Button {
                    appState.cacheFile(item)
                } label: {
                    Label("Cache File", systemImage: "arrow.down.circle")
                        .frame(maxWidth: .infinity)
                }
                .buttonStyle(PillButtonStyle(dark: dark))
                .help("Keep this file on this Mac (encrypted) so it opens without the network")
            }
        }
        .task(id: "\(item.id)-\(appState.cacheRevision)") {
            isCached = await source.isCached(item)
        }
    }
}

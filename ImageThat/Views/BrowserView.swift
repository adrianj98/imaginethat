import SwiftUI

public struct BrowserView: View {
    @EnvironmentObject var appState: AppState
    @ObservedObject private var shortcuts = ShortcutManager.shared

    private let columns = [
        GridItem(.adaptive(minimum: 160, maximum: 200), spacing: 12)
    ]

    public init() {}

    public var body: some View {
        ScrollView {
            LazyVGrid(columns: columns, spacing: 12) {
                ForEach(Array(appState.mediaItems.enumerated()), id: \.element.id) { index, item in
                    ThumbnailCell(item: item)
                        .onTapGesture(count: 2) {
                            appState.enterDetail(at: index)
                        }
                        .onTapGesture(count: 1) {
                            appState.selectedIndex = index
                        }
                        .overlay(
                            RoundedRectangle(cornerRadius: 8)
                                .stroke(appState.selectedIndex == index ? Color.accentColor : Color.clear, lineWidth: 3)
                        )
                }
            }
            .padding()
        }
        .focusable()
        .focusEffectDisabled()
        .onKeyPress(.return) {
            appState.viewMode = .detail
            return .handled
        }
    }
}

public struct ThumbnailCell: View {
    @EnvironmentObject var appState: AppState
    public let item: MediaItem
    @State private var thumbnail: NSImage?

    public init(item: MediaItem) {
        self.item = item
    }

    public var body: some View {
        VStack(spacing: 4) {
            ZStack {
                RoundedRectangle(cornerRadius: 8)
                    .fill(Color.gray.opacity(0.1))
                    .aspectRatio(1, contentMode: .fit)

                if let thumbnail {
                    Image(nsImage: thumbnail)
                        .resizable()
                        .aspectRatio(contentMode: .fill)
                        .frame(minWidth: 0, maxWidth: .infinity, minHeight: 0, maxHeight: .infinity)
                        .clipShape(RoundedRectangle(cornerRadius: 8))
                } else {
                    ProgressView()
                }

                if item.mediaType == .video {
                    Image(systemName: "play.circle.fill")
                        .font(.system(size: 32))
                        .foregroundStyle(.white)
                        .shadow(radius: 2)
                }
            }
            .aspectRatio(1, contentMode: .fit)

            Text(item.name)
                .font(.caption)
                .lineLimit(1)
                .truncationMode(.middle)
        }
        .task(id: item.id) {
            thumbnail = nil
            thumbnail = await appState.loadThumbnail(for: item, size: CGSize(width: 200, height: 200))
        }
    }
}

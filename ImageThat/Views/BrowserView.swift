import SwiftUI
import UniformTypeIdentifiers

public struct BrowserView: View {
    @EnvironmentObject var appState: AppState
    @ObservedObject private var shortcuts = ShortcutManager.shared
    @State private var showNewFolderDialog = false
    @State private var newFolderName = ""

    private let columns = [
        GridItem(.adaptive(minimum: 160, maximum: 200), spacing: 12)
    ]

    public init() {}

    public var body: some View {
        VStack {
            ScrollView {
                LazyVGrid(columns: columns, spacing: 12) {
                    ForEach(Array(appState.mediaItems.enumerated()), id: \.element.id) { index, item in
                        ThumbnailCell(item: item, index: index, isInVault: appState.mediaSource is VaultSource)
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
            .dropDestination(for: URL.self) { urls, _ in
                guard appState.mediaSource is WritableMediaSource else { return false }
                appState.importFiles(urls, into: appState.mediaSource)
                return true
            } isTargeted: { _ in }
            .contextMenu {
                if appState.mediaSource is VaultSource {
                    Button("New Folder") {
                        showNewFolderDialog = true
                    }
                }
            }
        }
        .focusable()
        .focusEffectDisabled()
        .onKeyPress(.return) {
            appState.viewMode = .detail
            return .handled
        }
        .alert("New Folder", isPresented: $showNewFolderDialog) {
            TextField("Folder name", text: $newFolderName)
            Button("Create") {
                appState.createFolder(named: newFolderName)
                newFolderName = ""
            }
            Button("Cancel", role: .cancel) {
                newFolderName = ""
            }
        }
    }
}

public struct ThumbnailCell: View {
    @EnvironmentObject var appState: AppState
    public let item: MediaItem
    public let index: Int
    public let isInVault: Bool
    @State private var thumbnail: NSImage?
    @State private var thumbnailLoadFailed = false
    @State private var lastItemID: MediaItem.ID?

    private var isSelected: Bool { appState.selectedIndex == index }

    public init(item: MediaItem, index: Int = 0, isInVault: Bool = false) {
        self.item = item
        self.index = index
        self.isInVault = isInVault
    }

    var cellContent: some View {
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
                } else if thumbnailLoadFailed {
                    Image(systemName: "exclamationmark.triangle")
                        .font(.system(size: 28))
                        .foregroundStyle(.secondary)
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
        .contextMenu {
            if isInVault {
                Button("Delete", role: .destructive) {
                    appState.deleteItem(item)
                }
            }
        }
    }

    public var body: some View {
        Group {
            if let transfer = MediaTransfer(for: item, source: appState.mediaSource) {
                cellContent.draggable(transfer)
            } else {
                cellContent
            }
        }
        .task(id: "\(item.id)-\(isSelected)", priority: isSelected ? .userInitiated : .utility) {
            if item.id != lastItemID {
                thumbnail = nil
                thumbnailLoadFailed = false
                lastItemID = item.id
            }
            if thumbnail == nil && !thumbnailLoadFailed {
                let result = await appState.loadThumbnail(for: item, size: CGSize(width: 200, height: 200))
                if let result {
                    thumbnail = result
                } else {
                    thumbnailLoadFailed = true
                }
            }
        }
    }
}

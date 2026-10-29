import SwiftUI
import UniformTypeIdentifiers

public struct BrowserView: View {
    @EnvironmentObject var appState: AppState
    @ObservedObject private var shortcuts = ShortcutManager.shared
    @State private var showNewFolderDialog = false
    @State private var newFolderName = ""
    @State private var searchText = ""
    @AppStorage("browserShowInfo") private var showInfo = true
    @State private var selectedPreview: NSImage?

    private let columns = [
        GridItem(.adaptive(minimum: 150, maximum: 220), spacing: 12)
    ]

    /// Items matching the search, with their index in `appState.mediaItems`.
    private var visibleItems: [(index: Int, item: MediaItem)] {
        appState.mediaItems.enumerated()
            .filter { searchText.isEmpty || $0.element.name.localizedCaseInsensitiveContains(searchText) }
            .map { (index: $0.offset, item: $0.element) }
    }

    public init() {}

    public var body: some View {
        ZStack(alignment: .top) {
            Glass.lightCanvas.ignoresSafeArea()

            ScrollView {
                LazyVGrid(columns: columns, spacing: 12) {
                    ForEach(visibleItems, id: \.item.id) { entry in
                        ThumbnailCell(item: entry.item, index: entry.index, isInVault: appState.mediaSource is VaultSource)
                            .onTapGesture(count: 2) {
                                appState.enterDetail(at: entry.index)
                            }
                            .onTapGesture(count: 1) {
                                appState.selectedIndex = entry.index
                            }
                    }
                }
                .padding(.top, 76)
                .padding(.leading, 16)
                .padding(.trailing, showInfo ? 300 : 16)
                .padding(.bottom, 24)
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

            header
                .padding(.top, 16)
                .padding(.leading, 16)
                .padding(.trailing, showInfo ? 300 : 16)

            if showInfo, let item = appState.selectedItem {
                ItemInfoCard(item: item, preview: selectedPreview,
                             footnote: appState.mediaSource is VaultSource ? "Decrypted in memory only" : nil)
                    .frame(width: 268)
                    .padding(16)
                    .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topTrailing)
                    .transition(.move(edge: .trailing).combined(with: .opacity))
                    .task(id: item.id) {
                        selectedPreview = nil
                        selectedPreview = await appState.loadThumbnail(for: item, size: CGSize(width: 200, height: 200))
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

    // MARK: - Floating toolbar

    private var header: some View {
        HStack(spacing: 10) {
            SidebarToggle()

            if appState.canGoUp {
                HStack(spacing: 2) {
                    GlassIconButton(systemImage: "chevron.left", help: "Go Up") { appState.goUp() }
                }
                .padding(.horizontal, 4)
                .glassCapsule()
            }

            breadcrumb
                .padding(.horizontal, 14)
                .glassCapsule()

            Spacer(minLength: 0)

            HStack(spacing: 8) {
                Image(systemName: "magnifyingglass")
                    .font(.system(size: 12, weight: .semibold))
                    .foregroundStyle(Glass.label)
                TextField("Search", text: $searchText)
                    .textFieldStyle(.plain)
                    .font(.system(size: 13))
            }
            .padding(.horizontal, 14)
            .frame(width: 200)
            .glassCapsule()

            HStack(spacing: 2) {
                if appState.mediaSource is WritableMediaSource {
                    GlassIconButton(systemImage: "square.and.arrow.down", help: "Import") { importFiles() }
                    GlassIconButton(systemImage: "folder.badge.plus", help: "New Folder") { showNewFolderDialog = true }
                }
                GlassIconButton(systemImage: "sidebar.right", help: showInfo ? "Hide Info" : "Show Info", isOn: showInfo) {
                    withAnimation(.easeOut(duration: 0.2)) { showInfo.toggle() }
                }
                .keyboardShortcut("i", modifiers: [.option, .command])
            }
            .padding(.horizontal, 4)
            .glassCapsule()
        }
        .frame(height: 48)
    }

    private var breadcrumb: some View {
        let source = appState.mediaSource
        let components = (source?.currentPath ?? "").split(separator: "/").map(String.init)
        let root = source?.rootDisplayName ?? ""
        let title = components.last ?? root
        let parent = components.count > 1 ? components[components.count - 2] : (components.isEmpty ? nil : root)
        return HStack(spacing: 6) {
            if let parent, !parent.isEmpty {
                Text(parent).font(.system(size: 13)).foregroundStyle(.secondary).lineLimit(1)
                Image(systemName: "chevron.right")
                    .font(.system(size: 9, weight: .bold))
                    .foregroundStyle(.tertiary)
            }
            Text(title).font(.system(size: 15, weight: .semibold)).lineLimit(1)
            Text(appState.mediaItems.count == 1 ? "1 item" : "\(appState.mediaItems.count) items")
                .font(.system(size: 12))
                .foregroundStyle(.secondary)
                .padding(.leading, 6)
                .fixedSize()
        }
    }

    private func importFiles() {
        let panel = NSOpenPanel()
        panel.canChooseFiles = true
        panel.canChooseDirectories = false
        panel.allowsMultipleSelection = true
        panel.message = "Choose photos and videos to import"
        guard panel.runModal() == .OK, !panel.urls.isEmpty else { return }
        appState.importFiles(panel.urls, into: appState.mediaSource)
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
        ZStack {
            RoundedRectangle(cornerRadius: 12, style: .continuous)
                .fill(Color.black.opacity(0.05))

            if let thumbnail {
                Image(nsImage: thumbnail)
                    .resizable()
                    .aspectRatio(contentMode: .fill)
                    .frame(minWidth: 0, maxWidth: .infinity, minHeight: 0, maxHeight: .infinity)
            } else if thumbnailLoadFailed {
                Image(systemName: "exclamationmark.triangle")
                    .font(.system(size: 28))
                    .foregroundStyle(.secondary)
            } else {
                ProgressView()
            }
        }
        .aspectRatio(1, contentMode: .fit)
        .clipShape(RoundedRectangle(cornerRadius: 12, style: .continuous))
        .overlay(alignment: .bottomLeading) {
            if item.mediaType == .video {
                Image(systemName: "play.fill")
                    .font(.system(size: 9, weight: .bold))
                    .frame(width: 22, height: 22)
                    .background(.regularMaterial, in: Circle())
                    .padding(10)
            }
        }
        .overlay(alignment: .topTrailing) {
            if isSelected {
                Image(systemName: "checkmark")
                    .font(.system(size: 10, weight: .heavy))
                    .foregroundStyle(.white)
                    .frame(width: 22, height: 22)
                    .background(Circle().fill(Color.accentColor))
                    .overlay(Circle().stroke(Color.white.opacity(0.9), lineWidth: 2))
                    .padding(10)
            }
        }
        .overlay(
            RoundedRectangle(cornerRadius: 12, style: .continuous)
                .stroke(isSelected ? Color.accentColor : Color.clear, lineWidth: 3)
        )
        .shadow(color: .black.opacity(isSelected ? 0.18 : 0.08), radius: isSelected ? 12 : 1.5, y: isSelected ? 8 : 1)
        .scaleEffect(isSelected ? 0.97 : 1)
        .animation(.easeOut(duration: 0.16), value: isSelected)
        .help(item.name)
        .accessibilityLabel(item.name)
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

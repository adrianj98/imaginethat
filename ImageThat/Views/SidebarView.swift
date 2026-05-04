import SwiftUI

public struct SidebarView: View {
    @EnvironmentObject var appState: AppState
    @State private var dragTargetID: UUID?

    public init() {}

    public var body: some View {
        List(selection: $appState.selectedSidebarItem) {
            if !appState.favorites.isEmpty {
                Section("Favorites") {
                    ForEach(appState.favorites) { item in
                        Label(item.name, systemImage: "star.fill")
                            .tag(item)
                            .padding(.vertical, 4)
                            .padding(.horizontal, 6)
                            .contextMenu {
                                Button("Remove from Favorites", role: .destructive) {
                                    appState.removeFavorite(item)
                                }
                            }
                    }
                }
            }

            Section(appState.favorites.isEmpty ? "" : "Folders") {
                OutlineGroup(appState.sidebarItems, children: \.optionalChildren) { item in
                    folderRow(item)
                }
            }
        }
        .listStyle(.sidebar)
        .navigationTitle("Folders")
        .onChange(of: appState.selectedSidebarItem) { _, newValue in
            guard let item = newValue else { return }
            if appState.favorites.contains(where: { $0.id == item.id }) {
                appState.openDirectory(url: item.url)
            } else {
                appState.navigateToSidebarItem(item)
            }
        }
    }

    @ViewBuilder
    private func folderRow(_ item: SidebarItem) -> some View {
        Label(item.name, systemImage: item.children.isEmpty ? "folder" : "folder.fill")
            .tag(item)
            .padding(.vertical, 4)
            .padding(.horizontal, 6)
            .background(dragTargetID == item.id ? Color.accentColor.opacity(0.2) : Color.clear)
            .cornerRadius(6)
            .dropDestination(for: MediaTransfer.self) { transfers, _ in
                defer { dragTargetID = nil }
                guard let transfer = transfers.first else { return false }
                appState.handleTransferDrop(transfer, to: item)
                return true
            } isTargeted: { isTargeted in
                dragTargetID = isTargeted ? item.id : nil
            }
            .dropDestination(for: URL.self) { urls, _ in
                defer { dragTargetID = nil }
                guard item.vaultPath != nil else { return false }
                appState.importFiles(urls, into: appState.mediaSource)
                return true
            } isTargeted: { isTargeted in
                dragTargetID = isTargeted ? item.id : nil
            }
            .contextMenu {
                Button("Add to Favorites") {
                    appState.addFavorite(url: item.url)
                }
                if item.vaultPath != nil {
                    Divider()
                    Button("Delete Folder", role: .destructive) {
                        appState.deleteSidebarFolder(item)
                    }
                }
            }
    }
}

extension SidebarItem {
    public var optionalChildren: [SidebarItem]? {
        children.isEmpty ? nil : children
    }
}

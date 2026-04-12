import SwiftUI

public struct SidebarView: View {
    @EnvironmentObject var appState: AppState
    @State private var dragTargetID: UUID?

    public init() {}

    public var body: some View {
        List(appState.sidebarItems, children: \.optionalChildren, selection: $appState.selectedSidebarItem) { item in
            Label(item.name, systemImage: item.children.isEmpty ? "folder" : "folder.fill")
                .tag(item)
                .padding(.vertical, 4)
                .padding(.horizontal, 6)
                .background(dragTargetID == item.id ? Color.accentColor.opacity(0.2) : Color.clear)
                .cornerRadius(6)
                .dropDestination(for: String.self) { droppedPaths, _ in
                    defer { dragTargetID = nil }
                    guard let vaultPath = item.vaultPath,
                          let path = droppedPaths.first,
                          let vaultSource = appState.mediaSource as? VaultSource,
                          vaultSource.bridge.vaultPath == item.url else { return false }
                    let sourceItem = appState.mediaItems.first { $0.url.path == path }
                    guard let sourceItem else { return false }
                    appState.moveItem(sourceItem, to: vaultPath)
                    return true
                } isTargeted: { isTargeted in
                    dragTargetID = isTargeted ? item.id : nil
                }
                .contextMenu {
                    if item.vaultPath != nil {
                        Button("Delete Folder", role: .destructive) {
                            appState.deleteSidebarFolder(item)
                        }
                    }
                }
        }
        .listStyle(.sidebar)
        .navigationTitle("Folders")
        .onChange(of: appState.selectedSidebarItem) { _, newValue in
            guard let item = newValue else { return }
            appState.navigateToSidebarItem(item)
        }
    }
}

extension SidebarItem {
    public var optionalChildren: [SidebarItem]? {
        children.isEmpty ? nil : children
    }
}

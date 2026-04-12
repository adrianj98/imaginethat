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

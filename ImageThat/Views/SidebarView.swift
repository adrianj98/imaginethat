import SwiftUI

public struct SidebarView: View {
    @EnvironmentObject var appState: AppState

    public init() {}

    public var body: some View {
        List(appState.sidebarItems, children: \.optionalChildren, selection: $appState.selectedSidebarItem) { item in
            Label(item.name, systemImage: item.children.isEmpty ? "folder" : "folder.fill")
                .tag(item)
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

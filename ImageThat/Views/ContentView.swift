import SwiftUI

public struct ContentView: View {
    @EnvironmentObject var appState: AppState

    public init() {}

    @State private var sidebarVisibility: NavigationSplitViewVisibility = .automatic

    public var body: some View {
        NavigationSplitView(columnVisibility: $sidebarVisibility) {
            SidebarView()
        } detail: {
            Group {
                if appState.showVaultUnlock {
                    VaultUnlockSheet()
                } else if appState.mediaItems.isEmpty && !appState.isLoading {
                    emptyState
                } else if appState.isLoading {
                    ProgressView("Loading...")
                } else {
                    switch appState.viewMode {
                    case .grid:
                        BrowserView()
                    case .detail:
                        DetailView()
                    }
                }
            }
        }
        .navigationSplitViewColumnWidth(min: 180, ideal: 220, max: 350)
        .frame(minWidth: 800, minHeight: 600)
        .onChange(of: appState.viewMode) { _, newValue in
            sidebarVisibility = newValue == .detail ? .detailOnly : .automatic
        }
        .alert("Error", isPresented: .init(
            get: { appState.errorMessage != nil },
            set: { if !$0 { appState.errorMessage = nil } }
        )) {
            Button("OK") { appState.errorMessage = nil }
        } message: {
            Text(appState.errorMessage ?? "")
        }
        .toolbar {
            ToolbarItemGroup(placement: .primaryAction) {
                if appState.canGoUp {
                    Button(action: { appState.goUp() }) {
                        Label("Go Up", systemImage: "chevron.up")
                    }
                }
                Button(action: { appState.openFolder() }) {
                    Label("Open Folder", systemImage: "folder")
                }
            }
            ToolbarItemGroup(placement: .principal) {
                if appState.viewMode == .detail {
                    Button(action: { appState.exitDetail() }) {
                        Label("Back", systemImage: "square.grid.2x2")
                    }
                    if let item = appState.selectedItem {
                        Text(item.name)
                            .font(.headline)
                            .lineLimit(1)
                    }
                    Text(appState.currentItemLabel)
                        .foregroundStyle(.secondary)
                        .font(.subheadline)
                }
            }
        }
    }

    private var emptyState: some View {
        VStack(spacing: 16) {
            Image(systemName: "photo.on.rectangle.angled")
                .font(.system(size: 64))
                .foregroundStyle(.tertiary)
            Text("Open a folder to browse media")
                .font(.title3)
                .foregroundStyle(.secondary)
            Button("Open Folder") { appState.openFolder() }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}

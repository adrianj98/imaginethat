import SwiftUI

public struct ContentView: View {
    @EnvironmentObject var appState: AppState
    @State private var sidebarBeforeDetail: NavigationSplitViewVisibility = .all

    public init() {}

    public var body: some View {
        innerBody
            .task { appState.restoreSession() }
    }

    @ViewBuilder
    private var innerBody: some View {
        NavigationSplitView(columnVisibility: $appState.sidebarVisibility) {
            SidebarView()
        } detail: {
            Group {
                if appState.showVaultUnlock {
                    VaultUnlockSheet()
                } else if appState.mediaItems.isEmpty && !appState.isLoading {
                    emptyState
                } else if appState.isLoading {
                    ProgressView("Loading...")
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                        .background(Glass.lightCanvas)
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
        .overlay(alignment: .bottom) {
            CopyProgressCard(transfers: appState.transfers)
        }
        .alert("Error", isPresented: .init(
            get: { appState.errorMessage != nil },
            set: { if !$0 { appState.errorMessage = nil } }
        )) {
            Button("OK") { appState.errorMessage = nil }
        } message: {
            Text(appState.errorMessage ?? "")
        }
        .toolbar(.hidden, for: .windowToolbar)
        .onChange(of: appState.viewMode) { _, mode in
            withAnimation(.easeOut(duration: 0.2)) {
                if mode == .detail {
                    sidebarBeforeDetail = appState.sidebarVisibility
                    appState.sidebarVisibility = .detailOnly
                } else {
                    appState.sidebarVisibility = sidebarBeforeDetail
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
                .buttonStyle(PillButtonStyle(kind: .prominent, height: 36))
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(Glass.lightCanvas)
    }
}

import SwiftUI

public struct ContentView: View {
    @EnvironmentObject var appState: AppState

    public init() {}

    public var body: some View {
        NavigationSplitView {
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
        .overlay(alignment: .bottom) {
            if let progress = appState.fileTransferProgress,
               let status = appState.fileTransferStatus {
                let isComplete = progress.current >= progress.total
                let elapsed = Date().timeIntervalSince(progress.startTime)

                VStack(spacing: 12) {
                    HStack {
                        Text(status)
                            .font(.subheadline)
                        Spacer()
                        Button(action: { appState.cancelFileTransfer() }) {
                            Image(systemName: "xmark.circle.fill")
                                .font(.system(size: 14))
                        }
                        .buttonStyle(.plain)
                    }

                    if isComplete {
                        // Show completion info
                        ProgressView(value: 1.0)
                        HStack {
                            Text("\(progress.total / 1_000_000) MB")
                                .font(.caption)
                            Spacer()
                            let seconds = max(elapsed, 0.1)
                            let mbPerSecond = Double(progress.total) / seconds / 1_000_000
                            Text(String(format: "%.1f MB/s", mbPerSecond))
                                .font(.caption)
                                .fontWeight(.semibold)
                                .foregroundStyle(.blue)
                            Spacer()
                            Text(String(format: "%.1fs", seconds))
                                .font(.caption)
                        }
                        .foregroundStyle(.secondary)
                    } else {
                        // Transfer in progress - show indeterminate progress with elapsed time
                        ProgressView()
                        HStack {
                            Text("\(progress.total / 1_000_000) MB total")
                                .font(.caption)
                            Spacer()
                            Text(String(format: "%.0fs elapsed", elapsed))
                                .font(.caption)
                        }
                        .foregroundStyle(.secondary)
                    }
                }
                .padding()
                .background(Color(nsColor: .controlBackgroundColor))
                .cornerRadius(8)
                .padding()
            }
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

import SwiftUI

public struct SidebarView: View {
    @EnvironmentObject var appState: AppState
    @State private var dragTargetID: UUID?

    public init() {}

    public var body: some View {
        List(selection: $appState.selectedSidebarItem) {
            favoritesSection
            foldersSection
            googleDriveSection
        }
        .listStyle(.sidebar)
        .navigationTitle("Folders")
        .sheet(isPresented: $appState.showGoogleDriveSetup) {
            GoogleDriveSetupView()
                .environmentObject(appState)
        }
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
    private var favoritesSection: some View {
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
    }

    @ViewBuilder
    private var foldersSection: some View {
        Section(appState.favorites.isEmpty ? "" : "Folders") {
            OutlineGroup(appState.sidebarItems, children: \.optionalChildren) { item in
                folderRow(item)
            }
        }
    }

    @ViewBuilder
    private var googleDriveSection: some View {
        Section {
            googleDriveSectionContent
        } header: {
            googleDriveSectionHeader
        }
    }

    @ViewBuilder
    private var googleDriveSectionContent: some View {
        if appState.googleDriveConnected {
            OutlineGroup(appState.googleDriveSidebarItems, children: \.optionalChildren) { item in
                folderRow(item)
            }
        } else if appState.isConnectingGoogleDrive {
            HStack {
                ProgressView().scaleEffect(0.7)
                Text("Connecting...").foregroundStyle(.secondary)
            }
        } else {
            Button {
                appState.showGoogleDriveSetup = true
            } label: {
                Label("Connect Google Drive", systemImage: "plus.circle")
            }
            .buttonStyle(.plain)
            .foregroundStyle(Color.accentColor)
        }
    }

    @ViewBuilder
    private var googleDriveSectionHeader: some View {
        HStack {
            Text("Google Drive")
            Spacer()
            if appState.googleDriveConnected {
                Button {
                    appState.disconnectGoogleDrive()
                } label: {
                    Image(systemName: "xmark.circle.fill")
                        .foregroundStyle(.secondary)
                }
                .buttonStyle(.plain)
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
                folderRowContextMenu(item)
            }
    }

    @ViewBuilder
    private func folderRowContextMenu(_ item: SidebarItem) -> some View {
        if item.url.scheme != "gdrive" {
            Button("Add to Favorites") {
                appState.addFavorite(url: item.url)
            }
        }
        if item.vaultPath != nil {
            Divider()
            Button("Delete Folder", role: .destructive) {
                appState.deleteSidebarFolder(item)
            }
        }
        if item.url.scheme == "gdrive", item.url.host != "root" {
            Divider()
            Button("Delete Folder", role: .destructive) {
                appState.deleteGoogleDriveSidebarFolder(item)
            }
        }
    }
}

extension SidebarItem {
    public var optionalChildren: [SidebarItem]? {
        children.isEmpty ? nil : children
    }
}

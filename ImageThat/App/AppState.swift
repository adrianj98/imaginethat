import SwiftUI
import CryptoKit
import UniformTypeIdentifiers
import Foundation

extension Notification.Name {
    public static let openExternalFile = Notification.Name("com.adrianm.imagethat.openExternalFile")
}

public enum ViewMode {
    case grid
    case detail
}

@MainActor
public class AppState: ObservableObject {
    @Published public var mediaSource: (any MediaSource)?
    @Published public var mediaItems: [MediaItem] = []
    @Published public var selectedIndex: Int = 0
    @Published public var viewMode: ViewMode = .grid
    @Published public var isLoading = false
    @Published public var errorMessage: String?
    @Published public var showVaultUnlock = false
    @Published public var pendingVaultURL: URL?
    private var pendingVaultRebuildSidebar = false
    /// When auto-routing through a vault root for a deep folder click, this
    /// holds the plain-text path within the vault to land on after unlock.
    private var pendingVaultSubpath: String?

    // Sidebar state
    @Published public var sidebarItems: [SidebarItem] = []
    @Published public var selectedSidebarItem: SidebarItem?
    /// Left column; the viewer collapses it and the grid restores it.
    @Published public var sidebarVisibility: NavigationSplitViewVisibility = .all

    // Google Drive state
    @Published public var googleDriveConnected = false
    @Published public var googleDriveSidebarItems: [SidebarItem] = []
    @Published public var showGoogleDriveSetup = false
    @Published public var isConnectingGoogleDrive = false

    // imagethat-web servers
    @Published public var serverSidebarItems: [SidebarItem] = []
    @Published public var showAddServer = false
    /// Name shown in the unlock prompt when the pending URL is not a path.
    @Published public var pendingVaultDisplayName: String?
    /// Clients keyed by server base URL string, kept for the session.
    private var serverClients: [String: RemoteServerClient] = [:]
    /// Remote vaults this session has unlocked, by RemoteLocation.passwordKey.
    private var unlockedRemoteVaults: Set<String> = []
    private static let serversKey = "imagethatWebServers"

    private let thumbnailCache = ThumbnailCache()
    private var loadGeneration = 0
    private var currentLoadTask: Task<Void, Never>?

    /// Preloaded full images for instant navigation (next item only, evicted on navigation)
    private var imagePreloadCache: [MediaItem.ID: NSImage] = [:]

    private static let lastFolderKey = "lastOpenedFolderURL"

    /// Navigation history for going back/up
    @Published public var navigationStack: [URL] = []

    /// Session password cache: vault path → password
    private var vaultPasswords: [String: String] = [:]
    /// Cached master key for the session (after Touch ID)
    private var masterKey: Data?

    /// Vault bridges keyed by vault path — kept alive for the session
    private var vaultBridges: [String: GocryptfsBridge] = [:]
    /// Drive vault sessions keyed by Drive folder ID
    private var driveVaultBridges: [String: GocryptfsBridge] = [:]

    // Favorites
    @Published public var favorites: [SidebarItem] = []
    private static let favoritesKey = "favoritesFolderPaths"

    /// File to select after loading a folder (set by openFile)
    @Published private(set) var pendingSelectionName: String?

    /// File transfer progress: (current bytes, total bytes, start time)
    @Published public var fileTransferProgress: (current: Int64, total: Int64, startTime: Date)? = nil
    /// Human-readable status during transfer
    @Published public var fileTransferStatus: String? = nil

    /// Files being cached for offline use (remote and Drive vaults), keyed by
    /// item URL, with their progress 0...1.
    @Published public private(set) var cacheProgress: [URL: Double] = [:]
    /// Bumped when a file is cached or uncached, so views re-ask the source.
    @Published public private(set) var cacheRevision = 0
    private var cacheTasks: [URL: Task<Void, Never>] = [:]
    /// Per unlocked remote vault (password key): a ciphertext-mode session with
    /// a file cache, which RemoteVaultSource reads cached files through.
    private var remoteCaches: [String: GocryptfsBridge] = [:]

    /// Current file transfer task (for cancellation)
    private var fileTransferTask: Task<Void, Never>? = nil

    public init() {
        NotificationCenter.default.addObserver(
            self,
            selector: #selector(handleExternalFileOpen(_:)),
            name: .openExternalFile,
            object: nil
        )
        NotificationCenter.default.addObserver(
            self,
            selector: #selector(handleFileTransferProgress(_:)),
            name: NSNotification.Name("com.adrianm.imagethat.fileTransferProgress"),
            object: nil
        )
    }

    @objc private func handleExternalFileOpen(_ notification: Notification) {
        guard let url = notification.object as? URL else { return }
        openFile(url: url)
    }

    @objc private func handleFileTransferProgress(_ notification: Notification) {
        guard let tuple = notification.object as? (current: Int64, total: Int64, startTime: Date) else { return }
        // Sources post this from their write task; @Published must change on the main thread.
        DispatchQueue.main.async { self.fileTransferProgress = tuple }
    }

    public func addFavorite(url: URL) {
        guard !favorites.contains(where: { $0.url == url }) else { return }
        favorites.append(SidebarItem(name: url.lastPathComponent, url: url, children: []))
        saveFavorites()
    }

    public func removeFavorite(_ item: SidebarItem) {
        favorites.removeAll { $0.url == item.url }
        saveFavorites()
    }

    private func saveFavorites() {
        AppSettings.shared.set(favorites.map { $0.url.path }, forKey: Self.favoritesKey)
    }

    private func loadFavorites() {
        let paths = AppSettings.shared.stringArray(forKey: Self.favoritesKey) ?? []
        favorites = paths
            .filter { FileManager.default.fileExists(atPath: $0) }
            .map { SidebarItem(name: URL(fileURLWithPath: $0).lastPathComponent, url: URL(fileURLWithPath: $0), children: []) }
    }

    /// Called on launch: open path from CLI args, else restore last folder.
    public func restoreSession() {
        loadFavorites()
        loadSavedPasswords()
        restoreGoogleDriveSession()
        restoreServers()
        let args = CommandLine.arguments
        if args.count > 1 {
            let url = URL(fileURLWithPath: args[1])
            openDirectory(url: url)
        } else if let url = AppSettings.shared.url(forKey: Self.lastFolderKey) {
            openDirectory(url: url)
        }
    }

    /// Load all saved vault passwords on app launch.
    /// - Plaintext mode: reads the JSON file directly, no Touch ID prompt.
    /// - Encrypted mode: fetches the master key from Keychain (triggers Touch ID).
    public func loadSavedPasswords() {
        guard PasswordStore.shared.hasPasswords else { return }

        if PasswordStore.encryptionDisabled {
            // No encryption, no biometric — just read the file.
            if let saved = try? PasswordStore.shared.loadAll() {
                for (path, password) in saved {
                    self.vaultPasswords[path] = password
                }
            }
            return
        }

        guard KeychainManager.shared.hasMasterKey() else { return }
        Task {
            do {
                guard let key = try KeychainManager.shared.getMasterKey() else { return }
                self.masterKey = key
                let saved = try PasswordStore.shared.loadAll(masterKey: key)
                for (path, password) in saved {
                    self.vaultPasswords[path] = password
                }
            } catch {
                // Touch ID cancelled or failed — user can enter passwords manually
            }
        }
    }

    /// Get or create the master key. Creates a new one if none exists.
    private func getOrCreateMasterKey() throws -> Data {
        if let key = masterKey { return key }
        if KeychainManager.shared.hasMasterKey() {
            if let key = try KeychainManager.shared.getMasterKey() {
                masterKey = key
                return key
            }
        }
        // Generate new master key
        let key = SymmetricKey(size: .bits256)
        let keyData = key.withUnsafeBytes { Data($0) }
        try KeychainManager.shared.saveMasterKey(keyData)
        masterKey = keyData
        return keyData
    }

    public var selectedItem: MediaItem? {
        guard mediaItems.indices.contains(selectedIndex) else { return nil }
        return mediaItems[selectedIndex]
    }

    public var nextItem: MediaItem? {
        guard selectedIndex + 1 < mediaItems.count else { return nil }
        return mediaItems[selectedIndex + 1]
    }

    public func preloadImage(for item: MediaItem) async {
        guard imagePreloadCache[item.id] == nil, item.mediaType == .image,
              let source = mediaSource else { return }
        guard let image = try? await source.loadImage(for: item) else { return }
        imagePreloadCache[item.id] = image
    }

    public func popPreloadedImage(for item: MediaItem) -> NSImage? {
        imagePreloadCache.removeValue(forKey: item.id)
    }

    public var currentItemLabel: String {
        guard !mediaItems.isEmpty else { return "" }
        return "\(selectedIndex + 1) of \(mediaItems.count)"
    }

    // MARK: - Open folder (auto-detects vaults)

    public func openFolder() {
        let panel = NSOpenPanel()
        panel.canChooseFiles = false
        panel.canChooseDirectories = true
        panel.allowsMultipleSelection = false
        panel.message = "Choose a folder to browse"

        guard panel.runModal() == .OK, let url = panel.url else { return }
        openDirectory(url: url)
    }

    /// Called from Cmd+O or toolbar — this is the only place that rebuilds the sidebar.
    public func openDirectory(url: URL) {
        if url.scheme == "gdrive" {
            navigateToGoogleDriveFolder(folderId: url.host ?? "root")
        } else if isVault(url) {
            tryUnlockVault(url: url, rebuildSidebar: true)
        } else {
            loadFolder(url: url, rebuildSidebar: true)
        }
    }

    /// Open a specific file from Finder, loading its parent folder and selecting the file
    public func openFile(url: URL) {
        let parentURL = url.deletingLastPathComponent()
        pendingSelectionName = url.lastPathComponent
        openDirectory(url: parentURL)
    }

    /// Called from sidebar selection — never rebuilds the sidebar, but lazily expands children.
    public func navigateToSidebarItem(_ item: SidebarItem) {
        if item.url.scheme == RemoteLocation.scheme {
            navigateToRemoteSidebarItem(item)
            return
        }
        if item.url.scheme == "gdrive" {
            expandGoogleDriveSidebarItem(item)
            if let vaultPath = item.vaultPath, let rootId = item.url.host {
                navigateToDriveVaultFolder(rootFolderId: rootId, path: vaultPath)
            } else {
                navigateToGoogleDriveFolder(folderId: item.url.host ?? "root")
            }
            return
        }

        // Lazily expand children in the sidebar if not yet loaded
        expandSidebarItem(item)

        if let vaultPath = item.vaultPath {
            // Look up the correct bridge for this vault (item.url is the vault root)
            guard let bridge = vaultBridges[item.url.path] else { return }
            if let current = mediaSource as? VaultSource, current.currentPath == vaultPath, current.bridge === bridge { return }
            let source = VaultSource(bridge: bridge, currentPath: vaultPath)
            self.mediaSource = source
            self.viewMode = .grid
            self.selectedIndex = 0
            loadMedia(from: source)
        } else {
            if let current = mediaSource as? FolderSource, current.rootURL == item.url { return }
            if isVault(item.url) {
                tryUnlockVault(url: item.url, rebuildSidebar: false)
            } else {
                loadFolder(url: item.url, rebuildSidebar: false)
            }
        }
    }

    /// Lazily load children for a sidebar item if it hasn't been expanded yet.
    private func expandSidebarItem(_ item: SidebarItem) {
        // Already has children — nothing to do
        if !item.children.isEmpty { return }

        let itemID = item.id
        let itemURL = item.url

        if let vaultPath = item.vaultPath {
            // Vault subfolder — load decrypted subfolders
            guard let bridge = vaultBridges[itemURL.path] else { return }
            Task.detached {
                let children = Self.vaultSubfolders(bridge: bridge, path: vaultPath, maxDepth: 1)
                await MainActor.run { [weak self] in
                    self?.updateSidebarChildren(parentID: itemID, children: children)
                }
            }
        } else {
            if isVault(itemURL) {
                // Vault folder — expand with decrypted names if unlocked
                guard let bridge = vaultBridges[itemURL.path] else { return }
                Task.detached {
                    let children = Self.vaultSubfolders(bridge: bridge, path: "", maxDepth: 1)
                    await MainActor.run { [weak self] in
                        self?.updateSidebarChildrenByURL(parentURL: itemURL, children: children)
                    }
                }
            } else {
                // Plain folder — load subfolders
                Task.detached {
                    let children = Self.subfolders(of: itemURL, maxDepth: 1)
                    await MainActor.run { [weak self] in
                        self?.updateSidebarChildren(parentID: itemID, children: children)
                    }
                }
            }
        }
    }

    /// Update a sidebar item's children in the tree by ID.
    private func updateSidebarChildren(parentID: UUID, children: [SidebarItem]) {
        guard !children.isEmpty else { return }
        sidebarItems = sidebarItems.map { updateChildren(in: $0, parentID: parentID, children: children) }
    }

    private func updateChildren(in item: SidebarItem, parentID: UUID, children: [SidebarItem]) -> SidebarItem {
        if item.id == parentID {
            var updated = item
            updated.children = children
            return updated
        }
        var updated = item
        updated.children = item.children.map { updateChildren(in: $0, parentID: parentID, children: children) }
        return updated
    }

    private func tryUnlockVault(url: URL, rebuildSidebar: Bool) {
        // 1. Session cache
        if let password = vaultPasswords[url.path] {
            unlockVault(url: url, password: password, rebuildSidebar: rebuildSidebar)
            return
        }

        // 2. Try to load saved passwords (encrypted mode with Touch ID, or plaintext mode)
        if PasswordStore.shared.hasPasswords {
            Task {
                do {
                    var saved: [String: String] = [:]

                    if PasswordStore.encryptionDisabled {
                        // Plaintext mode — no Touch ID needed
                        saved = try PasswordStore.shared.loadAll(masterKey: nil)
                    } else if KeychainManager.shared.hasMasterKey() {
                        // Encrypted mode — ask for Touch ID
                        guard let key = try KeychainManager.shared.getMasterKey() else {
                            await MainActor.run {
                                self.pendingVaultURL = url
                                self.pendingVaultRebuildSidebar = rebuildSidebar
                                self.showVaultUnlock = true
                            }
                            return
                        }
                        self.masterKey = key
                        saved = try PasswordStore.shared.loadAll(masterKey: key)
                    }

                    // Cache all loaded passwords
                    for (path, password) in saved {
                        self.vaultPasswords[path] = password
                    }

                    // Check if this vault was saved
                    if let password = saved[url.path] {
                        unlockVault(url: url, password: password, rebuildSidebar: rebuildSidebar)
                    } else {
                        await MainActor.run {
                            self.pendingVaultURL = url
                            self.pendingVaultRebuildSidebar = rebuildSidebar
                            self.showVaultUnlock = true
                        }
                    }
                } catch {
                    await MainActor.run {
                        self.pendingVaultURL = url
                        self.pendingVaultRebuildSidebar = rebuildSidebar
                        self.showVaultUnlock = true
                    }
                }
            }
            return
        }

        // 3. No saved passwords — show password prompt
        pendingVaultURL = url
        pendingVaultRebuildSidebar = rebuildSidebar
        showVaultUnlock = true
    }

    public func unlockVault(password: String, saveToKeychain: Bool = true) {
        guard let url = pendingVaultURL else { return }
        let rebuild = pendingVaultRebuildSidebar
        let subpath = pendingVaultSubpath
        pendingVaultURL = nil
        pendingVaultRebuildSidebar = false
        pendingVaultSubpath = nil
        pendingVaultDisplayName = nil
        if let remote = RemoteLocation.parse(url), let vaultID = remote.vaultID {
            Task { await self.unlockRemoteVault(server: remote.server, vaultID: vaultID, password: password,
                                                saveToKeychain: saveToKeychain, subpath: subpath) }
            return
        }
        if let remote = RemoteLocation.parse(url), remote.vaultID == nil {
            Task { await self.loginToServer(remote.server, accessPassword: password, save: saveToKeychain) }
            return
        }
        if url.scheme == "gdrive", let folderId = url.host {
            Task { await self.unlockDriveVault(folderId: folderId, password: password, saveToKeychain: saveToKeychain, subpath: subpath) }
            return
        }
        unlockVault(url: url, password: password, rebuildSidebar: rebuild, saveToKeychain: saveToKeychain)
    }

    // MARK: - File operations

    public func moveItem(_ item: MediaItem, to destinationPath: String) {
        guard let source = mediaSource as? WritableMediaSource else { return }
        Task {
            do {
                try await source.moveItem(item, to: destinationPath)
                self.loadMedia(from: mediaSource ?? source)
            } catch {
                self.errorMessage = error.localizedDescription
            }
        }
    }

    public func createFolder(named name: String) {
        guard let source = mediaSource as? WritableMediaSource else { return }
        Task {
            do {
                try await source.createDirectory(named: name)
                // Rebuild sidebar to show the new folder
                if let vaultSource = source as? VaultSource {
                    self.buildVaultSidebar(bridge: vaultSource.bridge, rootName: vaultSource.bridge.vaultPath.lastPathComponent)
                } else if let folderSource = source as? FolderSource {
                    let rootName = folderSource.rootURL.lastPathComponent
                    let children = Self.subfolders(of: folderSource.rootURL, maxDepth: 1)
                    let rootItem = SidebarItem(name: rootName, url: folderSource.rootURL, children: children)
                    self.sidebarItems = [rootItem]
                } else if let driveVault = source as? DriveVaultSource {
                    self.refreshDriveVaultSidebar(bridge: driveVault.bridge, path: driveVault.currentPath)
                    self.loadMedia(from: driveVault)
                } else if source is GoogleDriveSource {
                    await self.buildGoogleDriveSidebar()
                } else if let remote = source as? RemoteVaultSource {
                    self.refreshRemoteVaultSidebar(remote)
                }
            } catch {
                self.errorMessage = error.localizedDescription
            }
        }
    }

    public func deleteItem(_ item: MediaItem) {
        guard let source = mediaSource as? WritableMediaSource else { return }
        Task {
            do {
                try await source.deleteItem(item)
                self.loadMedia(from: mediaSource ?? source)
            } catch {
                self.errorMessage = error.localizedDescription
            }
        }
    }

    public func deleteSidebarFolder(_ sidebarItem: SidebarItem) {
        if sidebarItem.url.scheme == RemoteLocation.scheme {
            deleteRemoteSidebarFolder(sidebarItem)
            return
        }
        guard let vaultPath = sidebarItem.vaultPath,
              let bridge = vaultBridges[sidebarItem.url.path] else { return }

        Task {
            do {
                let source = VaultSource(bridge: bridge, currentPath: vaultPath)
                try await source.deleteCurrentDirectory()
                self.buildVaultSidebar(bridge: bridge, rootName: bridge.vaultPath.lastPathComponent)
                // If we were viewing that folder, go back to root
                if let vaultSource = self.mediaSource as? VaultSource,
                   vaultSource.currentPath == vaultPath {
                    self.openDirectory(url: sidebarItem.url)
                }
            } catch {
                self.errorMessage = error.localizedDescription
            }
        }
    }

    // MARK: - Cross-source file transfers

    /// Import external files (from Finder) into the current media source
    public func importFiles(_ urls: [URL], into source: (any MediaSource)?) {
        guard let dest = source as? WritableMediaSource else {
            print("importFiles: source is not WritableMediaSource")
            return
        }
        print("importFiles: importing \(urls.count) files")

        // Cancel any existing transfer
        fileTransferTask?.cancel()

        fileTransferTask = Task.detached { [weak self] in
            guard let self else { return }
            for url in urls {
                do {
                    print("importFiles: processing \(url.lastPathComponent)")
                    let name = url.lastPathComponent
                    let destPath: String
                    if let vaultSource = dest as? VaultSource {
                        destPath = vaultSource.currentPath.isEmpty ? name : "\(vaultSource.currentPath)/\(name)"
                    } else if let driveVault = dest as? DriveVaultSource {
                        destPath = driveVault.currentPath.isEmpty ? name : "\(driveVault.currentPath)/\(name)"
                    } else if let remote = dest as? RemoteVaultSource {
                        destPath = remote.currentPath.isEmpty ? name : "\(remote.currentPath)/\(name)"
                    } else {
                        destPath = name
                    }

                    let localSource = FolderSource(rootURL: url.deletingLastPathComponent())
                    let item = MediaItem(url: url)
                    let attrs = try FileManager.default.attributesOfItem(atPath: url.path)
                    let size = (attrs[.size] as? Int64) ?? 0

                    let startTime = Date()
                    await MainActor.run {
                        self.fileTransferStatus = "Copying \(name)..."
                        self.fileTransferProgress = (0, size, startTime)
                    }

                    print("importFiles: copying \(name) (\(size) bytes) to \(destPath)")

                    if let bridge = (dest as? VaultSource)?.bridge ?? (dest as? DriveVaultSource)?.bridge {
                        // Local and Drive vaults copy in the Go engine, which
                        // retries and resumes on read errors (files on a
                        // network share) and streams into Drive.
                        try await Self.engineCopy(.file(url), into: bridge, at: destPath)
                    } else {
                        // Use larger chunks (1MB) for better throughput
                        let chunkSize = 1_048_576  // 1MB chunks
                        let sourceChunks = localSource.readChunks(for: item, chunkSize: chunkSize)

                        // Pass chunks directly to destination.
                        // Destination tracks write progress and posts notifications.
                        try await dest.writeChunks(sourceChunks, to: destPath, totalSize: size)
                    }

                    print("importFiles: successfully copied \(name)")
                } catch {
                    print("importFiles: error - \(error)")
                    await MainActor.run {
                        self.errorMessage = error.localizedDescription
                        self.fileTransferStatus = nil
                        self.fileTransferProgress = nil
                    }
                }
            }
            await MainActor.run {
                self.fileTransferStatus = nil
                self.fileTransferProgress = nil
                self.loadMedia(from: source ?? dest)
            }
        }
    }

    /// Cancel the current file transfer
    public func cancelFileTransfer() {
        fileTransferTask?.cancel()
        fileTransferTask = nil
        fileTransferStatus = nil
        fileTransferProgress = nil
    }

    /// Handle a MediaTransfer drop onto a sidebar folder
    public func handleTransferDrop(_ transfer: MediaTransfer, to sidebarItem: SidebarItem) {
        if sidebarItem.url.scheme == RemoteLocation.scheme || RemoteLocation.parse(URL(string: transfer.sourceRootPath) ?? URL(fileURLWithPath: "/")) != nil {
            handleRemoteTransferDrop(transfer, to: sidebarItem)
            return
        }
        guard let destVaultPath = sidebarItem.vaultPath,
              let destBridge = vaultBridges[sidebarItem.url.path] else { return }
        let destSource = VaultSource(bridge: destBridge, currentPath: destVaultPath)

        // Same vault → move (preserve existing behaviour)
        if transfer.isVault && transfer.sourceRootPath == sidebarItem.url.path {
            guard let sourceItem = mediaItems.first(where: { $0.url.path == transfer.itemRelativePath }) else { return }
            moveItem(sourceItem, to: destVaultPath)
            return
        }

        // Cross-source → copy in the Go engine (retries and resumes on read
        // errors; the file appears only once complete)
        Task {
            do {
                let from: EngineCopySource
                if transfer.isVault, let bridge = vaultBridges[transfer.sourceRootPath] {
                    from = .vault(bridge, transfer.itemRelativePath)
                } else {
                    let rootURL = URL(fileURLWithPath: transfer.sourceRootPath)
                    from = .file(rootURL.appendingPathComponent(transfer.itemRelativePath))
                }

                let destFileName = (transfer.itemRelativePath as NSString).lastPathComponent
                let destPath = destVaultPath.isEmpty ? destFileName : "\(destVaultPath)/\(destFileName)"
                try await Self.engineCopy(from, into: destBridge, at: destPath)
                self.loadMedia(from: mediaSource ?? destSource)
            } catch {
                self.errorMessage = error.localizedDescription
            }
        }
    }

    // MARK: - Directory navigation

    public var canGoUp: Bool {
        !navigationStack.isEmpty
    }

    public func goUp() {
        guard let parentURL = navigationStack.popLast() else { return }
        // Don't push to stack again — we're going back
        loadFolderWithoutPush(url: parentURL)
    }

    // MARK: - Image navigation

    public func navigateNext() {
        guard selectedIndex < mediaItems.count - 1 else { return }
        selectedIndex += 1
    }

    public func navigatePrevious() {
        guard selectedIndex > 0 else { return }
        selectedIndex -= 1
    }

    public func enterDetail(at index: Int) {
        selectedIndex = index
        viewMode = .detail
    }

    public func exitDetail() {
        viewMode = .grid
    }

    public var isSourceWritable: Bool {
        mediaSource is WritableMediaSource
    }

    public func invalidateThumbnail(for item: MediaItem) {
        // Re-trigger BrowserView's ThumbnailCell task by momentarily replacing the item
        if let index = mediaItems.firstIndex(where: { $0.id == item.id }) {
            mediaItems[index] = item
        }
    }

    public func insertMediaItem(_ item: MediaItem, after existing: MediaItem) {
        if let index = mediaItems.firstIndex(where: { $0.id == existing.id }) {
            mediaItems.insert(item, at: index + 1)
        }
    }

    public func loadThumbnail(for item: MediaItem, size: CGSize) async -> NSImage? {
        // 1. Decoded images for the visible grid (memory only)
        if let cached = thumbnailCache.get(for: item.id) {
            return cached
        }

        guard let source = mediaSource else { return nil }
        let maxEdge = Int(max(size.width, size.height) * 2) // retina

        // 2. Vault sources: the engine's caches (encrypted memory, disk sealed
        //    with the vault key) and its own decoders for JPEG/PNG/GIF/WebP/BMP.
        let engine = Self.thumbnailer(for: source)
        if let engine,
           let jpeg = try? engine.thumbnail(path: item.url.path, maxEdge: maxEdge),
           let image = NSImage(data: jpeg) {
            thumbnailCache.set(image, for: item.id)
            return image
        }

        // 3. Render here (plain folders, Drive, remote servers; HEIC, RAW and
        //    video for vaults) and hand vault previews back to the engine.
        do {
            let thumb = try await source.loadThumbnail(for: item, size: size)
            thumbnailCache.set(thumb, for: item.id)
            if let engine, let jpeg = thumb.jpegData(compressionFactor: 0.8) {
                try? engine.storeThumbnail(jpeg, path: item.url.path, maxEdge: maxEdge)
            }
            return thumb
        } catch {
            return nil
        }
    }

    /// The engine session behind a vault source, which owns its preview caches.
    private static func thumbnailer(for source: any MediaSource) -> (any VaultThumbnailing)? {
        if let v = source as? VaultSource { return v.bridge }
        if let d = source as? DriveVaultSource { return d.bridge }
        return nil
    }

    /// Point a freshly unlocked session's preview caches at the app's cache
    /// directory. Entries are sealed with the vault's own key, so the directory
    /// is shared by every vault and unreadable without each one's password.
    private func configureThumbnails(_ bridge: any VaultThumbnailing, sourceID: String) {
        do {
            try bridge.configureThumbnails(sourceID: sourceID, diskDir: AppPaths.thumbnailCache, memBytes: 0, diskBytes: 0)
        } catch {
            print("thumbnail cache unavailable for \(sourceID): \(error)")
        }
    }

    // MARK: - Private

    private func isVault(_ url: URL) -> Bool {
        FileManager.default.fileExists(atPath: url.appendingPathComponent("gocryptfs.conf").path)
    }

    private func loadFolderWithoutPush(url: URL) {
        loadFolder(url: url, rebuildSidebar: true)
    }

    private func loadFolder(url: URL, rebuildSidebar: Bool) {
        AppSettings.shared.setURL(url, forKey: Self.lastFolderKey)
        let source = FolderSource(rootURL: url)
        self.mediaSource = source
        self.viewMode = .grid
        self.selectedIndex = 0
        loadMedia(from: source)

        if rebuildSidebar {
            let rootName = url.lastPathComponent
            Task.detached {
                let children = Self.subfolders(of: url, maxDepth: 1)
                await MainActor.run { [weak self] in
                    guard let self else { return }
                    let rootItem = SidebarItem(name: rootName, url: url, children: children)
                    self.sidebarItems = [rootItem]
                }
            }
        }
    }

    private func unlockVault(url: URL, password: String, rebuildSidebar: Bool = true, saveToKeychain: Bool = true) {
        isLoading = true
        Task {
            do {
                let bridge = try GocryptfsBridge(vaultPath: url, password: password)
                configureThumbnails(bridge, sourceID: url.path)
                vaultPasswords[url.path] = password
                // Save password to file if user opted in
                if saveToKeychain {
                    if PasswordStore.encryptionDisabled {
                        // Plaintext mode — save without Touch ID
                        try? PasswordStore.shared.save(password: password, forVault: url.path, masterKey: nil)
                    } else {
                        // Encrypted mode — get or create master key
                        if let key = try? getOrCreateMasterKey() {
                            try? PasswordStore.shared.save(password: password, forVault: url.path, masterKey: key)
                        }
                    }
                }
                vaultBridges[url.path] = bridge
                let source = VaultSource(bridge: bridge)
                self.mediaSource = source
                self.viewMode = .grid
                self.selectedIndex = 0
                if rebuildSidebar {
                    buildVaultSidebar(bridge: bridge, rootName: url.lastPathComponent)
                } else {
                    // Expand the vault's children in the existing sidebar
                    expandVaultInSidebar(bridge: bridge, vaultURL: url)
                }
                loadMedia(from: source)
            } catch {
                self.errorMessage = error.localizedDescription
                self.isLoading = false
            }
        }
    }

    private func loadMedia(from source: any MediaSource) {
        // Cancel any in-flight load
        currentLoadTask?.cancel()
        loadGeneration += 1
        let myGeneration = loadGeneration

        errorMessage = nil
        // Don't clear mediaItems immediately — keep showing previous content
        // until new content is ready, to avoid flashing empty state

        // Show loading indicator only if it takes more than 200ms
        let loadingTask = Task {
            try? await Task.sleep(for: .milliseconds(200))
            if !Task.isCancelled && myGeneration == self.loadGeneration {
                self.isLoading = true
            }
        }

        currentLoadTask = Task {
            do {
                let items = try await source.listMedia()
                loadingTask.cancel()
                // Only apply results if this is still the current load
                guard myGeneration == self.loadGeneration else { return }
                self.mediaItems = items

                // Resolve pending selection if set
                if let name = self.pendingSelectionName,
                   let index = items.firstIndex(where: { $0.name == name }) {
                    self.selectedIndex = index
                    self.viewMode = .detail
                }
                self.pendingSelectionName = nil
                self.isLoading = false
            } catch {
                loadingTask.cancel()
                guard myGeneration == self.loadGeneration else { return }
                self.errorMessage = error.localizedDescription
                self.isLoading = false
            }
        }
    }

    /// After unlocking a vault via sidebar click, expand its children in the sidebar tree.
    private func expandVaultInSidebar(bridge: GocryptfsBridge, vaultURL: URL) {
        Task.detached {
            let children = Self.vaultSubfolders(bridge: bridge, path: "", maxDepth: 1)
            await MainActor.run { [weak self] in
                self?.updateSidebarChildrenByURL(parentURL: vaultURL, children: children)
            }
        }
    }

    private func updateSidebarChildrenByURL(parentURL: URL, children: [SidebarItem]) {
        guard !children.isEmpty else { return }
        sidebarItems = sidebarItems.map { updateChildrenByURL(in: $0, parentURL: parentURL, children: children) }
    }

    private func updateChildrenByURL(in item: SidebarItem, parentURL: URL, children: [SidebarItem]) -> SidebarItem {
        if item.url == parentURL && item.vaultPath == nil {
            var updated = item
            updated.children = children
            return updated
        }
        var updated = item
        updated.children = item.children.map { updateChildrenByURL(in: $0, parentURL: parentURL, children: children) }
        return updated
    }

    // MARK: - Sidebar building (off main thread, depth-limited)

    nonisolated private static func subfolders(of url: URL, maxDepth: Int) -> [SidebarItem] {
        guard maxDepth > 0 else { return [] }
        let fm = FileManager.default
        guard let contents = try? fm.contentsOfDirectory(at: url, includingPropertiesForKeys: [.isDirectoryKey], options: [.skipsHiddenFiles]) else {
            return []
        }
        return contents
            .filter { (try? $0.resourceValues(forKeys: [.isDirectoryKey]).isDirectory) == true }
            .sorted { $0.lastPathComponent.localizedCaseInsensitiveCompare($1.lastPathComponent) == .orderedAscending }
            .map { subURL in
                // Don't recurse into vault directories — their contents are encrypted
                let isVault = fm.fileExists(atPath: subURL.appendingPathComponent("gocryptfs.conf").path)
                let children = isVault ? [] : subfolders(of: subURL, maxDepth: maxDepth - 1)
                return SidebarItem(name: subURL.lastPathComponent, url: subURL, children: children)
            }
    }

    private func buildVaultSidebar(bridge: GocryptfsBridge, rootName: String) {
        let vaultURL = bridge.vaultPath
        Task.detached { [weak self] in
            let children = Self.vaultSubfolders(bridge: bridge, path: "", maxDepth: 3)
            let rootItem = SidebarItem(name: rootName, url: vaultURL, vaultPath: "", children: children)
            await MainActor.run { [weak self] in
                guard let self else { return }
                self.sidebarItems = [rootItem]
                self.selectedSidebarItem = rootItem
            }
        }
    }

    nonisolated private static func vaultSubfolders(bridge: GocryptfsBridge, path: String, maxDepth: Int) -> [SidebarItem] {
        guard maxDepth > 0 else { return [] }
        let vaultSource = VaultSource(bridge: bridge, currentPath: path)
        guard let subdirs = try? vaultSource.listSubdirectories(path: path) else { return [] }
        return subdirs.map { dir in
            SidebarItem(
                name: dir.name,
                url: bridge.vaultPath,
                vaultPath: dir.path,
                children: vaultSubfolders(bridge: bridge, path: dir.path, maxDepth: maxDepth - 1)
            )
        }
    }

    // MARK: - Google Drive

    private func restoreGoogleDriveSession() {
        // Use Task.detached so the restore doesn't sit behind a busy MainActor
        // (e.g. while the sidebar/detail are still building). With a plain
        // `Task { ... }` the inherited @MainActor isolation made restore stall
        // for many seconds at app launch, leaving the Drive section stuck on
        // "Connect Google Drive" even when a refresh token was saved.
        Task.detached { [weak self] in
            AppPaths.removeLegacyDriveCaches()
            GoogleDriveService.shared.loadSavedCredentials()
            guard GoogleDriveService.shared.isConnected else { return }
            guard let self else { return }
            await MainActor.run { self.googleDriveConnected = true }
            await self.buildGoogleDriveSidebar()
        }
    }

    public func connectGoogleDrive(clientId: String, clientSecret: String) {
        isConnectingGoogleDrive = true
        Task {
            do {
                try await GoogleDriveService.shared.authorize(clientId: clientId, clientSecret: clientSecret)
                googleDriveConnected = true
                showGoogleDriveSetup = false
                await buildGoogleDriveSidebar()
            } catch GoogleDriveError.authCancelled {
                // User-initiated cancel — silent, no error banner.
            } catch {
                errorMessage = error.localizedDescription
            }
            isConnectingGoogleDrive = false
        }
    }

    public func cancelConnectGoogleDrive() {
        GoogleDriveService.shared.cancelAuthorization()
    }

    public func disconnectGoogleDrive() {
        GoogleDriveService.shared.disconnect()
        googleDriveConnected = false
        googleDriveSidebarItems = []
        if mediaSource is GoogleDriveSource {
            mediaSource = nil
            mediaItems = []
        }
    }

    func navigateToGoogleDriveFolder(folderId: String) {
        // Already unlocked: browse it, don't derive the key again.
        if driveVaultBridges[folderId] != nil {
            navigateToDriveVaultFolder(rootFolderId: folderId, path: "")
            return
        }
        Task {
            // A vault itself, or a folder INSIDE one: unlock the vault root and
            // land directly at the sub-path the user clicked (so they see
            // *their* photos, not the vault root).
            if let match = await GoogleDriveService.shared.findVaultRoot(forDescendant: folderId) {
                let subpath = match.descendantPath.joined(separator: "/")
                self.tryUnlockDriveVault(folderId: match.rootFolderId, navigateToSubpath: subpath.isEmpty ? nil : subpath)
                return
            }
            let source = GoogleDriveSource(folderId: folderId)
            self.mediaSource = source
            self.viewMode = .grid
            self.selectedIndex = 0
            self.loadMedia(from: source)
        }
    }

    private func buildGoogleDriveSidebar() async {
        do {
            let folders = try await GoogleDriveService.shared.listFolders(in: "root")
            let children = folders.map { folder in
                SidebarItem(name: folder.name, url: URL(string: "gdrive://\(folder.id)")!, children: [])
            }
            let root = SidebarItem(name: "My Drive", url: URL(string: "gdrive://root")!, children: children)
            googleDriveSidebarItems = [root]
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    private func expandGoogleDriveSidebarItem(_ item: SidebarItem) {
        guard item.children.isEmpty, let folderId = item.url.host else { return }
        let itemID = item.id
        // Inside a Drive vault the Drive folder names are gocryptfs ciphertext:
        // list the decrypted tree from the unlocked vault instead.
        if let bridge = driveVaultBridges[folderId] {
            loadDriveVaultSidebarChildren(bridge: bridge, path: item.vaultPath ?? "", parentID: itemID)
            return
        }
        if item.vaultPath != nil { return } // locked vault subfolder: nothing to show yet
        Task {
            do {
                let entries = try await GoogleDriveService.shared.list(in: folderId, mediaOnly: false)
                // A locked vault shows no children; they appear decrypted once
                // it is unlocked (see unlockDriveVault).
                if entries.contains(where: { !$0.isFolder && $0.name == "gocryptfs.conf" }) {
                    return
                }
                let folders = entries
                    .filter(\.isFolder)
                    .sorted { $0.name.localizedCaseInsensitiveCompare($1.name) == .orderedAscending }
                let children = folders.map { folder in
                    SidebarItem(name: folder.name, url: URL(string: "gdrive://\(folder.id)")!, children: [])
                }
                updateGoogleDriveSidebarChildren(parentID: itemID, children: children)
            } catch {
                errorMessage = error.localizedDescription
            }
        }
    }

    private func updateGoogleDriveSidebarChildren(parentID: UUID, children: [SidebarItem]) {
        func update(_ items: [SidebarItem]) -> [SidebarItem] {
            items.map { item in
                guard item.id == parentID else {
                    var copy = item
                    copy.children = update(item.children)
                    return copy
                }
                var copy = item
                copy.children = children
                return copy
            }
        }
        googleDriveSidebarItems = update(googleDriveSidebarItems)
    }

    /// Decrypted subfolders of `path` in an unlocked Drive vault, as sidebar
    /// items that keep the vault root's Drive ID in their URL and the
    /// plaintext vault path in `vaultPath`, like local vault items.
    private func loadDriveVaultSidebarChildren(bridge: GocryptfsBridge, path: String, parentID: UUID) {
        let rootId = bridge.driveFolderId
        Task.detached {
            // listDirectory blocks on Drive requests; keep it off the main actor.
            let entries = (try? bridge.listDirectory(path: path)) ?? []
            let children = entries
                .filter { $0.isDirectory && !$0.name.hasPrefix(".") }
                .sorted { $0.name.localizedCaseInsensitiveCompare($1.name) == .orderedAscending }
                .map { entry in
                    SidebarItem(name: entry.name, url: URL(string: "gdrive://\(rootId)")!,
                                vaultPath: path.isEmpty ? entry.name : "\(path)/\(entry.name)", children: [])
                }
            await MainActor.run { [weak self] in
                self?.updateGoogleDriveSidebarChildren(parentID: parentID, children: children)
            }
        }
    }

    /// Re-list the decrypted children of the sidebar item for `path` in the
    /// Drive vault rooted at `rootId` (after a folder is created or deleted).
    private func refreshDriveVaultSidebar(bridge: GocryptfsBridge, path: String) {
        let rootURL = URL(string: "gdrive://\(bridge.driveFolderId)")!
        let target = findItem(in: googleDriveSidebarItems) { item in
            item.url == rootURL && (item.vaultPath ?? "") == path
        }
        if let target { loadDriveVaultSidebarChildren(bridge: bridge, path: path, parentID: target.id) }
    }

    private func navigateToDriveVaultFolder(rootFolderId: String, path: String) {
        guard let bridge = driveVaultBridges[rootFolderId] else {
            tryUnlockDriveVault(folderId: rootFolderId, navigateToSubpath: path)
            return
        }
        if let current = mediaSource as? DriveVaultSource, current.bridge === bridge, current.currentPath == path { return }
        let source = DriveVaultSource(bridge: bridge, currentPath: path)
        mediaSource = source
        viewMode = .grid
        selectedIndex = 0
        loadMedia(from: source)
    }

    /// Delete a folder inside an unlocked Drive vault from the sidebar.
    public func deleteDriveVaultSidebarFolder(_ item: SidebarItem) {
        guard let rootId = item.url.host, let path = item.vaultPath, !path.isEmpty,
              let bridge = driveVaultBridges[rootId] else { return }
        Task {
            do {
                try await DriveVaultSource(bridge: bridge, currentPath: path).deleteCurrentDirectory()
                let parent = (path as NSString).deletingLastPathComponent
                self.refreshDriveVaultSidebar(bridge: bridge, path: parent == "." ? "" : parent)
                if let current = self.mediaSource as? DriveVaultSource, current.bridge === bridge,
                   current.currentPath == path || current.currentPath.hasPrefix(path + "/") {
                    self.navigateToDriveVaultFolder(rootFolderId: rootId, path: "")
                }
            } catch {
                self.errorMessage = error.localizedDescription
            }
        }
    }

    /// Maps a path of Drive folder names inside a vault (gocryptfs ciphertext,
    /// as `findVaultRoot` reports them) to the plaintext vault path.
    nonisolated private static func plainVaultPath(cipherComponents: [String], bridge: GocryptfsBridge) -> String? {
        var plain = ""
        for component in cipherComponents {
            guard let entries = try? bridge.listDirectory(path: plain) else { return nil }
            let match = entries.first { entry in
                guard entry.isDirectory else { return false }
                let candidate = plain.isEmpty ? entry.name : "\(plain)/\(entry.name)"
                return bridge.cipherPath(forPlainPath: candidate, isDir: true)
                    .map { ($0 as NSString).lastPathComponent == component } ?? false
            }
            guard let match else { return nil }
            plain = plain.isEmpty ? match.name : "\(plain)/\(match.name)"
        }
        return plain
    }

    public func deleteGoogleDriveSidebarFolder(_ item: SidebarItem) {
        guard let folderId = item.url.host else { return }
        Task {
            do {
                try await GoogleDriveService.shared.deleteFile(fileId: folderId)
                await buildGoogleDriveSidebar()
                if let driveSource = mediaSource as? GoogleDriveSource, driveSource.folderId == folderId {
                    navigateToGoogleDriveFolder(folderId: "root")
                }
            } catch {
                errorMessage = error.localizedDescription
            }
        }
    }

    // MARK: - Drive Vault unlock

    private func tryUnlockDriveVault(folderId: String, navigateToSubpath: String? = nil) {
        let cacheKey = "gdrive://\(folderId)"
        self.pendingVaultSubpath = navigateToSubpath

        // 1. Session cache
        if let password = vaultPasswords[cacheKey] {
            Task { await self.unlockDriveVault(folderId: folderId, password: password, saveToKeychain: false, subpath: navigateToSubpath) }
            return
        }

        // 2. Try saved passwords
        if PasswordStore.shared.hasPasswords {
            Task {
                do {
                    var saved: [String: String] = [:]
                    if PasswordStore.encryptionDisabled {
                        saved = try PasswordStore.shared.loadAll(masterKey: nil)
                    } else if KeychainManager.shared.hasMasterKey() {
                        guard let key = try KeychainManager.shared.getMasterKey() else {
                            await MainActor.run {
                                self.pendingVaultURL = URL(string: cacheKey)!
                                self.showVaultUnlock = true
                            }
                            return
                        }
                        self.masterKey = key
                        saved = try PasswordStore.shared.loadAll(masterKey: key)
                    }
                    for (path, password) in saved { self.vaultPasswords[path] = password }
                    if let password = saved[cacheKey] {
                        await self.unlockDriveVault(folderId: folderId, password: password, saveToKeychain: false, subpath: navigateToSubpath)
                    } else {
                        await MainActor.run {
                            self.pendingVaultURL = URL(string: cacheKey)!
                            self.showVaultUnlock = true
                        }
                    }
                } catch {
                    await MainActor.run {
                        self.pendingVaultURL = URL(string: cacheKey)!
                        self.showVaultUnlock = true
                    }
                }
            }
            return
        }

        // 3. No saved passwords — show prompt
        pendingVaultURL = URL(string: cacheKey)!
        showVaultUnlock = true
    }

    private func unlockDriveVault(folderId: String, password: String, saveToKeychain: Bool = true, subpath: String? = nil) async {
        isLoading = true
        do {
            let bridge = try await GoogleDriveService.shared.openVault(folderId: folderId, password: password)
            let cacheKey = "gdrive://\(folderId)"
            configureThumbnails(bridge, sourceID: cacheKey)
            vaultPasswords[cacheKey] = password
            if saveToKeychain {
                if PasswordStore.encryptionDisabled {
                    try? PasswordStore.shared.save(password: password, forVault: cacheKey, masterKey: nil)
                } else if let key = try? getOrCreateMasterKey() {
                    try? PasswordStore.shared.save(password: password, forVault: cacheKey, masterKey: key)
                }
            }
            driveVaultBridges[folderId] = bridge

            // A subpath from findVaultRoot is made of Drive folder names, which
            // inside a vault are ciphertext; map it to the plaintext path.
            var path = subpath ?? ""
            if !path.isEmpty {
                let given = path
                let resolved = await Task.detached { () -> String? in
                    if (try? bridge.listDirectory(path: given)) != nil { return given }
                    return Self.plainVaultPath(cipherComponents: given.split(separator: "/").map(String.init), bridge: bridge)
                }.value
                path = resolved ?? ""
            }

            // Show the vault's folders decrypted under its sidebar item.
            let rootURL = URL(string: cacheKey)!
            if let rootItem = findItem(in: googleDriveSidebarItems, where: { $0.url == rootURL && $0.vaultPath == nil }) {
                loadDriveVaultSidebarChildren(bridge: bridge, path: "", parentID: rootItem.id)
            }

            let source = DriveVaultSource(bridge: bridge, currentPath: path)
            self.mediaSource = source
            self.viewMode = .grid
            self.selectedIndex = 0
            loadMedia(from: source)
        } catch {
            self.errorMessage = error.localizedDescription
            self.isLoading = false
        }
    }
}

public struct SidebarItem: Identifiable, Hashable {
    public let id = UUID()
    public let name: String
    public let url: URL
    public var children: [SidebarItem]
    /// For vault items: the relative path within the vault. nil for plain folders.
    public var vaultPath: String?

    public init(name: String, url: URL, children: [SidebarItem]) {
        self.name = name
        self.url = url
        self.children = children
        self.vaultPath = nil
    }

    public init(name: String, url: URL, vaultPath: String, children: [SidebarItem]) {
        self.name = name
        self.url = url
        self.children = children
        self.vaultPath = vaultPath
    }

    public static func == (lhs: SidebarItem, rhs: SidebarItem) -> Bool {
        lhs.id == rhs.id
    }

    public func hash(into hasher: inout Hasher) {
        hasher.combine(id)
    }
}

// MARK: - imagethat-web servers

extension AppState {
    /// Display names of remote vaults, by RemoteLocation.passwordKey.
    private static var remoteVaultNames: [String: String] = [:]

    private func restoreServers() {
        for server in AppSettings.shared.stringArray(forKey: Self.serversKey) ?? [] {
            if let base = URL(string: server) { serverClients[server] = RemoteServerClient(baseURL: base) }
        }
        Task { await buildServerSidebar() }
    }

    /// Checks the server answers, then remembers it and lists its vaults.
    ///
    /// A client-decrypt server needs its access password first: without one
    /// this throws RemoteServerError.accessPasswordRequired so the sheet can
    /// ask for it. A good access password is remembered like a vault password.
    public func addServer(_ input: String, accessPassword: String? = nil,
                          saveAccessPassword: Bool = true) async throws {
        guard let server = Self.normalizeServerURL(input), let base = URL(string: server) else {
            throw RemoteServerError.invalidServerURL
        }
        let client = serverClients[server] ?? RemoteServerClient(baseURL: base)
        if try await client.serverMode() == .clientDecrypt, !client.hasAccessPassword {
            guard let accessPassword, !accessPassword.isEmpty else { throw RemoteServerError.accessPasswordRequired }
            try await client.login(accessPassword: accessPassword)
            rememberAccessPassword(accessPassword, server: server, save: saveAccessPassword)
        }
        _ = try await client.listVaults()
        serverClients[server] = client
        var saved = AppSettings.shared.stringArray(forKey: Self.serversKey) ?? []
        if !saved.contains(server) {
            saved.append(server)
            AppSettings.shared.set(saved, forKey: Self.serversKey)
        }
        await buildServerSidebar()
    }

    public func removeServer(_ item: SidebarItem) {
        guard let server = RemoteLocation.parse(item.url)?.server else { return }
        let client = serverClients.removeValue(forKey: server)
        // Logging out locks every vault this app unlocked on that server.
        Task { try? await client?.logout() }
        unlockedRemoteVaults = unlockedRemoteVaults.filter {
            URL(string: $0).flatMap(RemoteLocation.parse)?.server != server
        }
        remoteCaches = remoteCaches.filter {
            URL(string: $0.key).flatMap(RemoteLocation.parse)?.server != server
        }
        let saved = (AppSettings.shared.stringArray(forKey: Self.serversKey) ?? []).filter { $0 != server }
        AppSettings.shared.set(saved, forKey: Self.serversKey)
        serverSidebarItems.removeAll { RemoteLocation.parse($0.url)?.server == server }
        if let remote = mediaSource as? RemoteVaultSource, remote.client === client {
            mediaSource = nil
            mediaItems = []
        }
    }

    /// Accepts "nas.local:8347" as well as full URLs. Loopback defaults to
    /// http, anything else to https: the server refuses plain HTTP off loopback.
    public nonisolated static func normalizeServerURL(_ input: String) -> String? {
        var text = input.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else { return nil }
        if !text.contains("://") {
            let host = text.split(separator: ":").first.map(String.init) ?? text
            let loopback = host == "localhost" || host.hasPrefix("127.") || text.hasPrefix("[::1]")
            text = (loopback ? "http://" : "https://") + text
        }
        while text.hasSuffix("/") { text.removeLast() }
        guard let url = URL(string: text), let scheme = url.scheme, ["http", "https"].contains(scheme),
              url.host != nil else { return nil }
        return url.absoluteString
    }

    private static func serverDisplayName(_ server: String) -> String {
        guard let url = URL(string: server), let host = url.host else { return server }
        return url.port.map { "\(host):\($0)" } ?? host
    }

    private func buildServerSidebar() async {
        var items: [SidebarItem] = []
        for server in AppSettings.shared.stringArray(forKey: Self.serversKey) ?? [] {
            guard let client = serverClients[server] else { continue }
            // A client-decrypt server lists vaults only after login; use an
            // access password this session already holds, without prompting.
            if !client.hasAccessPassword, let access = vaultPasswords[Self.accessPasswordKey(server)],
               (try? await client.serverMode()) == .clientDecrypt {
                try? await client.login(accessPassword: access)
            }
            // An unreachable server still gets a row, so it can be removed.
            let vaults = (try? await client.listVaults()) ?? []
            let children = vaults.map { vault -> SidebarItem in
                Self.remoteVaultNames[RemoteLocation.passwordKey(server: server, vaultID: vault.id)] = vault.name
                return SidebarItem(name: vault.name, url: RemoteLocation.url(server: server, vaultID: vault.id),
                                   vaultPath: "", children: [])
            }
            items.append(SidebarItem(name: Self.serverDisplayName(server), url: RemoteLocation.url(server: server),
                                     children: children))
        }
        serverSidebarItems = items
    }

    private func navigateToRemoteSidebarItem(_ item: SidebarItem) {
        guard let location = RemoteLocation.parse(item.url) else { return }
        guard let vaultID = location.vaultID else {
            // A server row with no vaults may have been offline, or be a
            // client-decrypt server this session has not logged in to yet.
            if item.children.isEmpty { Task { await loginToServerIfNeeded(location.server) } }
            return
        }
        let path = item.vaultPath ?? ""
        let key = RemoteLocation.passwordKey(server: location.server, vaultID: vaultID)
        guard unlockedRemoteVaults.contains(key), let client = serverClients[location.server] else {
            tryUnlockRemoteVault(server: location.server, vaultID: vaultID, subpath: path)
            return
        }
        expandRemoteSidebarItem(item, client: client, vaultID: vaultID)
        if let current = mediaSource as? RemoteVaultSource, current.client === client,
           current.vaultID == vaultID, current.currentPath == path { return }
        showRemoteVault(client: client, vaultID: vaultID, key: key, path: path)
    }

    private func showRemoteVault(client: RemoteServerClient, vaultID: String, key: String, path: String) {
        let source = RemoteVaultSource(client: client, vaultID: vaultID,
                                       vaultName: Self.remoteVaultNames[key] ?? "Vault", currentPath: path,
                                       cache: remoteCaches[key])
        mediaSource = source
        viewMode = .grid
        selectedIndex = 0
        loadMedia(from: source)
    }

    private func tryUnlockRemoteVault(server: String, vaultID: String, subpath: String) {
        let key = RemoteLocation.passwordKey(server: server, vaultID: vaultID)

        // 1. Session cache
        if let password = vaultPasswords[key] {
            Task { await unlockRemoteVault(server: server, vaultID: vaultID, password: password,
                                           saveToKeychain: false, subpath: subpath) }
            return
        }

        // 2. Saved passwords, else 3. prompt
        Task {
            if PasswordStore.shared.hasPasswords, let password = await savedPassword(forKey: key) {
                await unlockRemoteVault(server: server, vaultID: vaultID, password: password,
                                        saveToKeychain: false, subpath: subpath)
            } else {
                promptForRemoteVault(key: key, subpath: subpath)
            }
        }
    }

    private func promptForRemoteVault(key: String, subpath: String) {
        pendingVaultURL = URL(string: key)
        pendingVaultSubpath = subpath
        pendingVaultDisplayName = Self.remoteVaultNames[key]
        showVaultUnlock = true
    }

    /// Loads remembered passwords (Touch ID in encrypted mode) and returns the one for `key`.
    private func savedPassword(forKey key: String) async -> String? {
        do {
            var saved: [String: String] = [:]
            if PasswordStore.encryptionDisabled {
                saved = try PasswordStore.shared.loadAll(masterKey: nil)
            } else if KeychainManager.shared.hasMasterKey() {
                guard let masterKey = try KeychainManager.shared.getMasterKey() else { return nil }
                self.masterKey = masterKey
                saved = try PasswordStore.shared.loadAll(masterKey: masterKey)
            }
            for (path, password) in saved { vaultPasswords[path] = password }
            return saved[key]
        } catch {
            return nil
        }
    }

    private func unlockRemoteVault(server: String, vaultID: String, password: String,
                                   saveToKeychain: Bool, subpath: String?) async {
        guard let client = serverClients[server] else { return }
        let key = RemoteLocation.passwordKey(server: server, vaultID: vaultID)
        isLoading = true
        do {
            try await client.unlock(vaultID: vaultID, password: password)
            vaultPasswords[key] = password
            if saveToKeychain {
                if PasswordStore.encryptionDisabled {
                    try? PasswordStore.shared.save(password: password, forVault: key, masterKey: nil)
                } else if let masterKey = try? getOrCreateMasterKey() {
                    try? PasswordStore.shared.save(password: password, forVault: key, masterKey: masterKey)
                }
            }
            unlockedRemoteVaults.insert(key)
            openRemoteCache(server: server, vaultID: vaultID, password: password, key: key)
            showRemoteVault(client: client, vaultID: vaultID, key: key, path: subpath ?? "")
            if let root = findItem(in: serverSidebarItems, where: { $0.url.absoluteString == key && $0.vaultPath == "" }) {
                expandRemoteSidebarItem(root, client: client, vaultID: vaultID)
            }
        } catch RemoteServerError.wrongPassword {
            // A remembered password may be stale; ask again.
            vaultPasswords[key] = nil
            isLoading = false
            errorMessage = RemoteServerError.wrongPassword.localizedDescription
            promptForRemoteVault(key: key, subpath: subpath ?? "")
        } catch RemoteServerError.accessPasswordRequired, RemoteServerError.wrongAccessPassword {
            // A client-decrypt server this session has not logged in to.
            isLoading = false
            vaultPasswords[key] = password
            await loginToServerIfNeeded(server)
        } catch {
            errorMessage = error.localizedDescription
            isLoading = false
        }
    }

    /// Key a server's access password is remembered under, beside vault
    /// passwords; the server URL without a vault cannot collide with them.
    private static func accessPasswordKey(_ server: String) -> String {
        RemoteLocation.url(server: server).absoluteString
    }

    private func rememberAccessPassword(_ password: String, server: String, save: Bool) {
        let key = Self.accessPasswordKey(server)
        vaultPasswords[key] = password
        guard save else { return }
        if PasswordStore.encryptionDisabled {
            try? PasswordStore.shared.save(password: password, forVault: key, masterKey: nil)
        } else if let masterKey = try? getOrCreateMasterKey() {
            try? PasswordStore.shared.save(password: password, forVault: key, masterKey: masterKey)
        }
    }

    /// For a client-decrypt server not yet logged in to: logs in with the
    /// session's or a saved access password, else asks for it.
    private func loginToServerIfNeeded(_ server: String) async {
        guard let client = serverClients[server] else { return }
        guard (try? await client.serverMode()) == .clientDecrypt, !client.hasAccessPassword else {
            await buildServerSidebar()
            return
        }
        let key = Self.accessPasswordKey(server)
        var password = vaultPasswords[key]
        if password == nil, PasswordStore.shared.hasPasswords {
            password = await savedPassword(forKey: key)
        }
        if let password {
            await loginToServer(server, accessPassword: password, save: false)
        } else {
            promptForServerLogin(server)
        }
    }

    private func loginToServer(_ server: String, accessPassword: String, save: Bool) async {
        guard let client = serverClients[server] else { return }
        do {
            try await client.login(accessPassword: accessPassword)
            rememberAccessPassword(accessPassword, server: server, save: save)
            await buildServerSidebar()
        } catch RemoteServerError.wrongAccessPassword {
            vaultPasswords[Self.accessPasswordKey(server)] = nil
            errorMessage = RemoteServerError.wrongAccessPassword.localizedDescription
            promptForServerLogin(server)
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    private func promptForServerLogin(_ server: String) {
        pendingVaultURL = RemoteLocation.url(server: server)
        pendingVaultSubpath = nil
        pendingVaultDisplayName = "Access password for \(Self.serverDisplayName(server))"
        showVaultUnlock = true
    }

    private func expandRemoteSidebarItem(_ item: SidebarItem, client: RemoteServerClient, vaultID: String) {
        guard item.children.isEmpty else { return }
        loadRemoteSidebarChildren(of: item, client: client, vaultID: vaultID)
    }

    private func loadRemoteSidebarChildren(of item: SidebarItem, client: RemoteServerClient, vaultID: String) {
        let itemID = item.id
        let vaultURL = item.url
        let source = RemoteVaultSource(client: client, vaultID: vaultID, vaultName: "", currentPath: item.vaultPath ?? "")
        Task {
            guard let dirs = try? await source.listSubdirectories() else { return }
            let children = dirs.map { SidebarItem(name: $0.name, url: vaultURL, vaultPath: $0.relativePath, children: []) }
            serverSidebarItems = serverSidebarItems.map { updateChildren(in: $0, parentID: itemID, children: children) }
        }
    }

    /// Re-lists the sidebar folders under a remote source's current folder, e.g. after mkdir.
    private func refreshRemoteVaultSidebar(_ remote: RemoteVaultSource) {
        let key = RemoteLocation.passwordKey(server: remote.client.baseURL.absoluteString, vaultID: remote.vaultID)
        guard let item = findItem(in: serverSidebarItems, where: {
            $0.url.absoluteString == key && $0.vaultPath == remote.currentPath
        }) else { return }
        loadRemoteSidebarChildren(of: item, client: remote.client, vaultID: remote.vaultID)
    }

    private func findItem(in items: [SidebarItem], where match: (SidebarItem) -> Bool) -> SidebarItem? {
        for item in items {
            if match(item) { return item }
            if let found = findItem(in: item.children, where: match) { return found }
        }
        return nil
    }

    private func deleteRemoteSidebarFolder(_ item: SidebarItem) {
        guard let location = RemoteLocation.parse(item.url), let vaultID = location.vaultID,
              let path = item.vaultPath, !path.isEmpty,
              let client = serverClients[location.server] else { return }
        Task {
            do {
                try await RemoteVaultSource(client: client, vaultID: vaultID, vaultName: "", currentPath: path)
                    .deleteCurrentDirectory()
                func remove(_ items: [SidebarItem]) -> [SidebarItem] {
                    items.filter { $0.id != item.id }.map { var copy = $0; copy.children = remove($0.children); return copy }
                }
                serverSidebarItems = remove(serverSidebarItems)
                if let current = mediaSource as? RemoteVaultSource, current.client === client,
                   current.vaultID == vaultID, current.currentPath == path || current.currentPath.hasPrefix(path + "/") {
                    let key = RemoteLocation.passwordKey(server: location.server, vaultID: vaultID)
                    showRemoteVault(client: client, vaultID: vaultID, key: key, path: "")
                }
            } catch {
                errorMessage = error.localizedDescription
            }
        }
    }

    /// Drag-and-drop where either end is a remote vault: a move within one
    /// remote vault, otherwise a streamed copy.
    private func handleRemoteTransferDrop(_ transfer: MediaTransfer, to sidebarItem: SidebarItem) {
        guard let destPath = sidebarItem.vaultPath else { return }

        let destSource: any WritableMediaSource
        if let location = RemoteLocation.parse(sidebarItem.url), let vaultID = location.vaultID,
           let client = serverClients[location.server] {
            destSource = RemoteVaultSource(client: client, vaultID: vaultID, vaultName: "", currentPath: destPath,
                                           cache: remoteCaches[RemoteLocation.passwordKey(server: location.server, vaultID: vaultID)])
        } else if let bridge = vaultBridges[sidebarItem.url.path] {
            destSource = VaultSource(bridge: bridge, currentPath: destPath)
        } else {
            return
        }

        let ext = (transfer.itemRelativePath as NSString).pathExtension.lowercased()
        let mediaType: MediaType = SupportedFormats.videoExtensions.contains(ext) ? .video : .image
        let sourceSource: any MediaSource
        let sourceItem: MediaItem
        // Set when the Go engine can read the source itself, for a copy into a
        // local vault that retries and resumes.
        var engineSource: EngineCopySource?
        if let location = URL(string: transfer.sourceRootPath).flatMap(RemoteLocation.parse),
           let vaultID = location.vaultID, let client = serverClients[location.server] {
            if transfer.sourceRootPath == sidebarItem.url.absoluteString,
               let item = mediaItems.first(where: { $0.url.path == transfer.itemRelativePath }) {
                moveItem(item, to: destPath)
                return
            }
            let cache = remoteCaches[RemoteLocation.passwordKey(server: location.server, vaultID: vaultID)]
            sourceSource = RemoteVaultSource(client: client, vaultID: vaultID, vaultName: "", cache: cache)
            sourceItem = MediaItem(name: (transfer.itemRelativePath as NSString).lastPathComponent,
                                   url: RemoteVaultSource.itemURL(transfer.itemRelativePath), mediaType: mediaType)
            if let cache { engineSource = .vault(cache, transfer.itemRelativePath) }
        } else if transfer.isVault, let bridge = vaultBridges[transfer.sourceRootPath] {
            sourceSource = VaultSource(bridge: bridge, currentPath: "")
            sourceItem = MediaItem(name: (transfer.itemRelativePath as NSString).lastPathComponent,
                                   url: RemoteVaultSource.itemURL(transfer.itemRelativePath), mediaType: mediaType)
            engineSource = .vault(bridge, transfer.itemRelativePath)
        } else if !transfer.isVault {
            let rootURL = URL(fileURLWithPath: transfer.sourceRootPath)
            sourceSource = FolderSource(rootURL: rootURL)
            sourceItem = MediaItem(url: rootURL.appendingPathComponent(transfer.itemRelativePath))
            engineSource = .file(sourceItem.url)
        } else {
            return
        }

        Task {
            do {
                let name = (transfer.itemRelativePath as NSString).lastPathComponent
                if let engineSource, let vaultDest = destSource as? VaultSource {
                    try await Self.engineCopy(engineSource, into: vaultDest.bridge, at: destPath.isEmpty ? name : "\(destPath)/\(name)")
                    if let current = mediaSource { loadMedia(from: current) }
                    return
                }
                let size = try await sourceSource.fileSize(for: sourceItem)
                let chunks = sourceSource.readChunks(for: sourceItem, chunkSize: 1_048_576)
                try await destSource.writeChunks(chunks, to: destPath.isEmpty ? name : "\(destPath)/\(name)", totalSize: size)
                if let current = mediaSource { loadMedia(from: current) }
            } catch {
                errorMessage = error.localizedDescription
            }
        }
    }

    // MARK: - Engine copies and offline caching

    enum EngineCopySource: Sendable {
        case file(URL)
        case vault(GocryptfsBridge, String)
    }

    /// Copies one file into a local or Drive vault through the Go engine.
    /// Reads that fail transiently (a network share, a remote server) are
    /// retried with backoff and resume where they stopped, and the file
    /// appears only once every byte is in. Progress goes out like
    /// VaultSource.writeChunks'.
    nonisolated static func engineCopy(_ from: EngineCopySource, into dest: GocryptfsBridge, at destPath: String) async throws {
        let startTime = Date()
        let report: @Sendable (Int64, Int64) -> Void = { done, total in
            NotificationCenter.default.post(name: NSNotification.Name("com.adrianm.imagethat.fileTransferProgress"),
                                            object: (current: done, total: total, startTime: startTime))
        }
        switch from {
        case .file(let url):
            try await GocryptfsTransfer.copyFile(url, into: dest, at: destPath, onProgress: report)
        case .vault(let source, let path):
            try await GocryptfsTransfer.copy(from: source, path: path, into: dest, at: destPath, onProgress: report)
        }
    }

    /// Opens the remote vault a second time in ciphertext mode with a file
    /// cache, on the client's engine connection (logged in already on a
    /// client-decrypt server). It costs a key derivation and a round trip, so
    /// it runs in the background; the current remote source picks it up when
    /// it is ready.
    private func openRemoteCache(server: String, vaultID: String, password: String, key: String) {
        guard remoteCaches[key] == nil, let client = serverClients[server] else { return }
        Task.detached(priority: .utility) { [weak self] in
            guard let bridge = try? await client.openVault(vaultID: vaultID, password: password,
                                                           cacheDir: AppPaths.remoteFilesCache) else { return }
            await self?.adoptRemoteCache(bridge, server: server, vaultID: vaultID, key: key)
        }
    }

    private func adoptRemoteCache(_ bridge: GocryptfsBridge, server: String, vaultID: String, key: String) {
        guard unlockedRemoteVaults.contains(key) else { return }
        remoteCaches[key] = bridge
        if let current = mediaSource as? RemoteVaultSource, current.cache == nil,
           current.vaultID == vaultID, current.client.baseURL.absoluteString == server {
            mediaSource = current.withCache(bridge)
            cacheRevision += 1
        }
    }

    public func isCaching(_ item: MediaItem) -> Bool { cacheProgress[item.url] != nil }

    /// Keeps a remote or Drive vault file on this Mac (as ciphertext), so it
    /// opens and plays without the network.
    public func cacheFile(_ item: MediaItem) {
        guard let source = mediaSource as? OfflineCaching, source.canCache, cacheProgress[item.url] == nil else { return }
        let url = item.url
        cacheProgress[url] = 0
        cacheTasks[url] = Task {
            do {
                try await source.cache(item) { done, total in
                    guard total > 0 else { return }
                    Task { @MainActor in
                        if self.cacheProgress[url] != nil { self.cacheProgress[url] = Double(done) / Double(total) }
                    }
                }
            } catch is CancellationError {
            } catch {
                errorMessage = "Couldn't cache \(item.name): \(error.localizedDescription)"
            }
            cacheProgress[url] = nil
            cacheTasks[url] = nil
            cacheRevision += 1
        }
    }

    public func cancelCaching(_ item: MediaItem) {
        cacheTasks[item.url]?.cancel()
    }

    public func uncacheFile(_ item: MediaItem) {
        guard let source = mediaSource as? OfflineCaching else { return }
        Task {
            await source.uncache(item)
            cacheRevision += 1
        }
    }
}

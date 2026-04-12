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

    // Sidebar state
    @Published public var sidebarItems: [SidebarItem] = []
    @Published public var selectedSidebarItem: SidebarItem?

    private let thumbnailCache = ThumbnailCache()
    private var loadGeneration = 0
    private var currentLoadTask: Task<Void, Never>?

    /// Navigation history for going back/up
    @Published public var navigationStack: [URL] = []

    /// Session password cache: vault path → password
    private var vaultPasswords: [String: String] = [:]
    /// Cached master key for the session (after Touch ID)
    private var masterKey: Data?

    /// Vault bridges keyed by vault path — kept alive for the session
    private var vaultBridges: [String: GocryptfsBridge] = [:]

    /// File to select after loading a folder (set by openFile)
    @Published private(set) var pendingSelectionName: String?

    /// File transfer progress: (current bytes, total bytes)
    @Published public var fileTransferProgress: (current: Int64, total: Int64)? = nil
    /// Human-readable status during transfer
    @Published public var fileTransferStatus: String? = nil

    public init() {
        NotificationCenter.default.addObserver(
            self,
            selector: #selector(handleExternalFileOpen(_:)),
            name: .openExternalFile,
            object: nil
        )
    }

    @objc private func handleExternalFileOpen(_ notification: Notification) {
        guard let url = notification.object as? URL else { return }
        openFile(url: url)
    }

    /// Load all saved vault passwords on app launch. Triggers Touch ID if passwords exist.
    public func loadSavedPasswords() {
        guard KeychainManager.shared.hasMasterKey(), PasswordStore.shared.hasPasswords else { return }
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
        if isVault(url) {
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
        pendingVaultURL = nil
        pendingVaultRebuildSidebar = false
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
        Task.detached { [weak self] in
            guard let self else { return }
            for url in urls {
                do {
                    print("importFiles: processing \(url.lastPathComponent)")
                    let name = url.lastPathComponent
                    let destPath: String
                    if let vaultSource = dest as? VaultSource {
                        destPath = vaultSource.currentPath.isEmpty ? name : "\(vaultSource.currentPath)/\(name)"
                    } else {
                        destPath = name
                    }

                    let localSource = FolderSource(rootURL: url.deletingLastPathComponent())
                    let item = MediaItem(url: url)
                    let attrs = try FileManager.default.attributesOfItem(atPath: url.path)
                    let size = (attrs[.size] as? Int64) ?? 0

                    await MainActor.run {
                        self.fileTransferStatus = "Copying \(name)..."
                        self.fileTransferProgress = (0, size)
                    }

                    print("importFiles: copying \(name) (\(size) bytes) to \(destPath)")

                    // Read chunks with progress tracking
                    var totalRead: Int64 = 0
                    let chunks = localSource.readChunks(for: item, chunkSize: 262_144)
                    var allData = Data()
                    allData.reserveCapacity(Int(size))

                    for try await chunk in chunks {
                        allData.append(chunk)
                        totalRead += Int64(chunk.count)

                        // Update progress on main thread — this keeps UI responsive
                        await MainActor.run {
                            self.fileTransferProgress = (totalRead, size)
                        }
                    }

                    // Create stream from accumulated data
                    let dataStream = AsyncThrowingStream<Data, Error> { continuation in
                        continuation.yield(allData)
                        continuation.finish()
                    }

                    // Write to destination
                    try await dest.writeChunks(dataStream, to: destPath, totalSize: size)

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

    /// Handle a MediaTransfer drop onto a sidebar folder
    public func handleTransferDrop(_ transfer: MediaTransfer, to sidebarItem: SidebarItem) {
        guard let destVaultPath = sidebarItem.vaultPath,
              let destBridge = vaultBridges[sidebarItem.url.path] else { return }
        let destSource = VaultSource(bridge: destBridge, currentPath: destVaultPath)

        // Same vault → move (preserve existing behaviour)
        if transfer.isVault && transfer.sourceRootPath == sidebarItem.url.path {
            guard let sourceItem = mediaItems.first(where: { $0.url.path == transfer.itemRelativePath }) else { return }
            moveItem(sourceItem, to: destVaultPath)
            return
        }

        // Cross-source → copy using readChunks / writeChunks
        Task {
            do {
                let sourceMediaSource: any MediaSource
                let sourceItem: MediaItem

                if transfer.isVault, let bridge = vaultBridges[transfer.sourceRootPath] {
                    // Vault source
                    let vs = VaultSource(bridge: bridge, currentPath: "")
                    sourceMediaSource = vs
                    let urlString = transfer.itemRelativePath.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? transfer.itemRelativePath
                    let url = URL(string: urlString) ?? URL(fileURLWithPath: transfer.itemRelativePath)
                    sourceItem = MediaItem(name: url.lastPathComponent, url: url, mediaType: .image)
                } else {
                    // Local folder source
                    let rootURL = URL(fileURLWithPath: transfer.sourceRootPath)
                    sourceMediaSource = FolderSource(rootURL: rootURL)
                    let fileURL = rootURL.appendingPathComponent(transfer.itemRelativePath)
                    sourceItem = MediaItem(url: fileURL)
                }

                let destFileName = (transfer.itemRelativePath as NSString).lastPathComponent
                let destPath = destVaultPath.isEmpty ? destFileName : "\(destVaultPath)/\(destFileName)"
                let fileSize = try await sourceMediaSource.fileSize(for: sourceItem)
                let chunks = sourceMediaSource.readChunks(for: sourceItem, chunkSize: 262_144)
                try await destSource.writeChunks(chunks, to: destPath, totalSize: fileSize)
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
        // 1. In-memory cache (fast)
        if let cached = thumbnailCache.get(for: item.id) {
            return cached
        }

        guard let source = mediaSource else { return nil }

        // 2. Persistent disk cache (vault sources only)
        if let vaultSource = source as? VaultSource {
            if let thumb = loadFromPersistentCache(item: item, bridge: vaultSource.bridge) {
                thumbnailCache.set(thumb, for: item.id)
                return thumb
            }
        }

        // 3. Generate from source (slow for vaults)
        do {
            let thumb = try await source.loadThumbnail(for: item, size: size)
            thumbnailCache.set(thumb, for: item.id)

            // Save to persistent cache for vault sources
            if let vaultSource = source as? VaultSource {
                saveToPersistentCache(thumb, item: item, bridge: vaultSource.bridge)
            }

            return thumb
        } catch {
            return nil
        }
    }

    private func loadFromPersistentCache(item: MediaItem, bridge: GocryptfsBridge) -> NSImage? {
        guard let fileSize = try? bridge.fileSize(path: item.url.path) else { return nil }
        return PersistentThumbnailCache.shared.get(
            vaultPath: bridge.vaultPath.path,
            filePath: item.url.path,
            expectedSize: fileSize,
            bridge: bridge
        )
    }

    private func saveToPersistentCache(_ image: NSImage, item: MediaItem, bridge: GocryptfsBridge) {
        guard let fileSize = try? bridge.fileSize(path: item.url.path) else { return }
        PersistentThumbnailCache.shared.set(
            image,
            vaultPath: bridge.vaultPath.path,
            filePath: item.url.path,
            fileSize: fileSize,
            bridge: bridge
        )
    }

    // MARK: - Private

    private func isVault(_ url: URL) -> Bool {
        FileManager.default.fileExists(atPath: url.appendingPathComponent("gocryptfs.conf").path)
    }

    private func loadFolderWithoutPush(url: URL) {
        loadFolder(url: url, rebuildSidebar: true)
    }

    private func loadFolder(url: URL, rebuildSidebar: Bool) {
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
            await MainActor.run {
                self?.sidebarItems = [rootItem]
                self?.selectedSidebarItem = rootItem
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

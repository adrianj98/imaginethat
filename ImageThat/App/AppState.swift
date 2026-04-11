import SwiftUI
import CryptoKit
import UniformTypeIdentifiers

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
    /// The most recently used vault bridge
    private var activeVaultBridge: GocryptfsBridge?

    public init() {}

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

    /// Called from sidebar selection — never rebuilds the sidebar, but lazily expands children.
    public func navigateToSidebarItem(_ item: SidebarItem) {
        // Lazily expand children in the sidebar if not yet loaded
        expandSidebarItem(item)

        if let vaultPath = item.vaultPath {
            // Look up the correct bridge for this vault (item.url is the vault root)
            guard let bridge = vaultBridges[item.url.path] ?? activeVaultBridge else { return }
            if let current = mediaSource as? VaultSource, current.currentPath == vaultPath, current.bridge === bridge { return }
            activeVaultBridge = bridge
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
            guard let bridge = activeVaultBridge else { return }
            Task.detached {
                let children = Self.vaultSubfolders(bridge: bridge, path: vaultPath, maxDepth: 1)
                await MainActor.run { [weak self] in
                    self?.updateSidebarChildren(parentID: itemID, children: children)
                }
            }
        } else {
            if isVault(itemURL) {
                // Vault folder — expand with decrypted names if unlocked
                guard let bridge = activeVaultBridge, bridge.vaultPath == itemURL else { return }
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
        // 2. Manual password prompt
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
        activeVaultBridge = nil
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
                // Save password to encrypted file if user opted in
                if saveToKeychain {
                    if let key = try? getOrCreateMasterKey() {
                        try? PasswordStore.shared.save(password: password, forVault: url.path, masterKey: key)
                    }
                }
                vaultBridges[url.path] = bridge
                activeVaultBridge = bridge
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
            await MainActor.run {
                let rootItem = SidebarItem(name: rootName, url: vaultURL, vaultPath: "", children: children)
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

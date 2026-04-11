import SwiftUI
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

    /// Active vault bridge (kept alive for subfolder navigation)
    private var activeVaultBridge: GocryptfsBridge?

    public init() {}

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

    /// Called from sidebar selection — never rebuilds the sidebar.
    public func navigateToSidebarItem(_ item: SidebarItem) {
        if let vaultPath = item.vaultPath {
            // Already-open vault subfolder navigation
            guard let bridge = activeVaultBridge else { return }
            if let current = mediaSource as? VaultSource, current.currentPath == vaultPath { return }
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

    private func tryUnlockVault(url: URL, rebuildSidebar: Bool) {
        // 1. Session cache
        if let password = vaultPasswords[url.path] {
            unlockVault(url: url, password: password, rebuildSidebar: rebuildSidebar)
            return
        }
        // 2. Keychain (triggers Touch ID)
        if KeychainManager.shared.hasPassword(forVault: url.path) {
            Task {
                do {
                    if let password = try KeychainManager.shared.getPassword(forVault: url.path) {
                        unlockVault(url: url, password: password, rebuildSidebar: rebuildSidebar)
                        return
                    }
                } catch { }
                pendingVaultURL = url
                showVaultUnlock = true
            }
            return
        }
        // 3. Manual password prompt
        pendingVaultURL = url
        showVaultUnlock = true
    }

    public func unlockVault(password: String, saveToKeychain: Bool = true) {
        guard let url = pendingVaultURL else { return }
        pendingVaultURL = nil
        unlockVault(url: url, password: password, rebuildSidebar: false, saveToKeychain: saveToKeychain)
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

        if rebuildSidebar {
            let rootName = url.lastPathComponent
            let rootItem = SidebarItem(name: rootName, url: url, children: [])
            sidebarItems = [rootItem]
            selectedSidebarItem = rootItem
            // Build sidebar children in background
            Task.detached {
                let children = Self.subfolders(of: url, maxDepth: 1)
                await MainActor.run { [weak self] in
                    guard let self else { return }
                    let updatedRoot = SidebarItem(name: rootName, url: url, children: children)
                    self.sidebarItems = [updatedRoot]
                }
            }
        }

        loadMedia(from: source)
    }

    private func unlockVault(url: URL, password: String, rebuildSidebar: Bool = true, saveToKeychain: Bool = true) {
        isLoading = true
        Task {
            do {
                let bridge = try GocryptfsBridge(vaultPath: url, password: password)
                vaultPasswords[url.path] = password
                if saveToKeychain {
                    try? KeychainManager.shared.savePassword(password, forVault: url.path)
                }
                activeVaultBridge = bridge
                let source = VaultSource(bridge: bridge)
                self.mediaSource = source
                self.viewMode = .grid
                self.selectedIndex = 0
                if rebuildSidebar {
                    buildVaultSidebar(bridge: bridge, rootName: url.lastPathComponent)
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
            .map { SidebarItem(name: $0.lastPathComponent, url: $0, children: subfolders(of: $0, maxDepth: maxDepth - 1)) }
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

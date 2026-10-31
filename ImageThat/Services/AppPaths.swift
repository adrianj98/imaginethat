import Foundation

/// Central location for all on-disk app data. Everything lives under `~/.imagethat/`
/// so it's discoverable, easily backed up, and survives rebuilds / `make clean`.
public enum AppPaths {
    /// `~/.imagethat`
    public static let root: URL = {
        let home = FileManager.default.homeDirectoryForCurrentUser
        let url = home.appendingPathComponent(".imagethat", isDirectory: true)
        try? FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
        return url
    }()

    /// `~/.imagethat/settings.plist` — file-backed settings store.
    public static let settingsFile: URL = root.appendingPathComponent("settings.plist")

    /// `~/.imagethat/cache` — root for all caches.
    public static let cacheRoot: URL = ensureDir(root.appendingPathComponent("cache", isDirectory: true))

    /// `~/.imagethat/cache/thumbnails`
    public static let thumbnailCache: URL = ensureDir(cacheRoot.appendingPathComponent("thumbnails", isDirectory: true))

    /// `~/.imagethat/cache/network` — the engine's network cache: ciphertext
    /// read from Google Drive, imagethat-web servers and vaults on network
    /// mounts, in one store with one size limit.
    public static let networkCache: URL = ensureDir(cacheRoot.appendingPathComponent("network", isDirectory: true))

    /// `~/.imagethat/passwords` — vault password store.
    public static let passwordsDir: URL = ensureDir(root.appendingPathComponent("passwords", isDirectory: true))


    private static func ensureDir(_ url: URL) -> URL {
        try? FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
        return url
    }
}

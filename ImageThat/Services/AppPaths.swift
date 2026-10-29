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

    /// `~/.imagethat/cache/drive-files` — full-file Drive cache.
    public static let driveFilesCache: URL = ensureDir(cacheRoot.appendingPathComponent("drive-files", isDirectory: true))

    /// `~/.imagethat/cache/drive-files-index.json`
    public static let driveFilesIndex: URL = cacheRoot.appendingPathComponent("drive-files-index.json")

    /// `~/.imagethat/cache/drive-ranges` — byte-range Drive cache.
    public static let driveRangesCache: URL = ensureDir(cacheRoot.appendingPathComponent("drive-ranges", isDirectory: true))

    /// `~/.imagethat/cache/drive-ranges-index.json`
    public static let driveRangesIndex: URL = cacheRoot.appendingPathComponent("drive-ranges-index.json")

    /// `~/.imagethat/cache/remote-files` — files from imagethat-web servers the
    /// user chose to keep, as ciphertext exactly as the server stores them.
    public static let remoteFilesCache: URL = ensureDir(cacheRoot.appendingPathComponent("remote-files", isDirectory: true))

    /// `~/.imagethat/passwords` — vault password store.
    public static let passwordsDir: URL = ensureDir(root.appendingPathComponent("passwords", isDirectory: true))

    private static func ensureDir(_ url: URL) -> URL {
        try? FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
        return url
    }
}

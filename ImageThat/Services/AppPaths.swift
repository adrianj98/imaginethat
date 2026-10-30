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

    /// `~/.imagethat/cache/drive` — the engine's Drive cache: ciphertext
    /// ranges of Drive vault files, shared by every Drive vault.
    public static let driveCache: URL = ensureDir(cacheRoot.appendingPathComponent("drive", isDirectory: true))

    /// `~/.imagethat/cache/remote-files` — files from imagethat-web servers the
    /// user chose to keep, as ciphertext exactly as the server stores them.
    public static let remoteFilesCache: URL = ensureDir(cacheRoot.appendingPathComponent("remote-files", isDirectory: true))

    /// `~/.imagethat/passwords` — vault password store.
    public static let passwordsDir: URL = ensureDir(root.appendingPathComponent("passwords", isDirectory: true))

    /// Deletes the Drive caches the app kept before the Go engine took Drive
    /// over (whole files and byte ranges, each with an index). Nothing reads
    /// them any more.
    public static func removeLegacyDriveCaches() {
        for name in ["drive-files", "drive-files-index.json", "drive-ranges", "drive-ranges-index.json"] {
            try? FileManager.default.removeItem(at: cacheRoot.appendingPathComponent(name))
        }
    }

    private static func ensureDir(_ url: URL) -> URL {
        try? FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
        return url
    }
}

import Foundation

/// Persistent LRU disk cache for Google Drive file downloads.
/// Files are stored under ~/Library/Caches/ImageThat/GoogleDrive/files/ keyed by Drive file ID.
/// Eviction is LRU by last-access time; maximum size is configurable via UserDefaults.
public actor DriveCache {
    public static let shared = DriveCache()

    private let fileDir: URL
    private let indexURL: URL
    private var index: [String: Entry] = [:]

    private struct Entry: Codable {
        let fileId: String
        var size: Int64
        let modifiedTime: String
        var lastAccessed: Date
    }

    private static let defaultMaxBytes: Int64 = 10 * 1_073_741_824  // 10 GB
    private static let maxBytesKey = "googleDriveCacheMaxBytes"

    public static var maxBytes: Int64 {
        get {
            let v = UserDefaults.standard.object(forKey: maxBytesKey) as? Int64 ?? 0
            return v > 0 ? v : defaultMaxBytes
        }
        set { UserDefaults.standard.set(newValue, forKey: maxBytesKey) }
    }

    private init() {
        let caches = FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask)[0]
        let base = caches.appendingPathComponent("ImageThat/GoogleDrive")
        let fd = base.appendingPathComponent("files")
        let iu = base.appendingPathComponent("index.json")
        fileDir = fd
        indexURL = iu
        try? FileManager.default.createDirectory(at: fd, withIntermediateDirectories: true)
        index = Self.loadIndexSync(fileDir: fd, indexURL: iu)
    }

    nonisolated private static func loadIndexSync(fileDir: URL, indexURL: URL) -> [String: Entry] {
        guard let data = try? Data(contentsOf: indexURL),
              let entries = try? JSONDecoder().decode([Entry].self, from: data) else { return [:] }
        var result = Dictionary(uniqueKeysWithValues: entries.map { ($0.fileId, $0) })
        for id in result.keys where !FileManager.default.fileExists(atPath: fileDir.appendingPathComponent(id).path) {
            result.removeValue(forKey: id)
        }
        return result
    }

    // MARK: - Public API

    public func get(fileId: String, modifiedTime: String) -> Data? {
        guard var entry = index[fileId], entry.modifiedTime == modifiedTime else { return nil }
        let file = fileDir.appendingPathComponent(fileId)
        guard let data = try? Data(contentsOf: file) else {
            index.removeValue(forKey: fileId)
            saveIndex()
            return nil
        }
        entry.lastAccessed = Date()
        index[fileId] = entry
        saveIndex()
        return data
    }

    public func store(fileId: String, modifiedTime: String, data: Data) {
        let file = fileDir.appendingPathComponent(fileId)
        guard (try? data.write(to: file)) != nil else { return }
        index[fileId] = Entry(fileId: fileId, size: Int64(data.count), modifiedTime: modifiedTime, lastAccessed: Date())
        saveIndex()
        evictIfNeeded()
    }

    public func invalidate(fileId: String) {
        try? FileManager.default.removeItem(at: fileDir.appendingPathComponent(fileId))
        index.removeValue(forKey: fileId)
        saveIndex()
    }

    public func currentSize() -> Int64 {
        index.values.reduce(0) { $0 + $1.size }
    }

    public func clear() {
        for entry in index.values {
            try? FileManager.default.removeItem(at: fileDir.appendingPathComponent(entry.fileId))
        }
        index = [:]
        saveIndex()
    }

    // MARK: - Eviction

    private func evictIfNeeded() {
        let limit = Self.maxBytes
        var total = currentSize()
        guard total > limit else { return }

        let sorted = index.values.sorted { $0.lastAccessed < $1.lastAccessed }
        for entry in sorted {
            guard total > limit else { break }
            try? FileManager.default.removeItem(at: fileDir.appendingPathComponent(entry.fileId))
            index.removeValue(forKey: entry.fileId)
            total -= entry.size
        }
        saveIndex()
    }

    // MARK: - Index persistence

    private func saveIndex() {
        guard let data = try? JSONEncoder().encode(Array(index.values)) else { return }
        try? data.write(to: indexURL)
    }
}

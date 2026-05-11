import Foundation

/// Persistent on-disk cache for byte-range reads of large Drive files.
/// Each entry is a pre-sized sparse file with an in-memory interval index.
/// Reads are fully synchronous (NSLock — no actor hop needed from VFS callbacks).
/// Writes update the index synchronously but persist to disk in the background.
final class DriveRangeCache: @unchecked Sendable {
    static let shared = DriveRangeCache()

    struct Interval: Codable {
        var start: Int64
        var end: Int64  // exclusive

        func covers(offset: Int64, length: Int64) -> Bool {
            start <= offset && end >= offset + length
        }

        func canMerge(_ other: Interval) -> Bool {
            start <= other.end && other.start <= end
        }

        func merged(with other: Interval) -> Interval {
            Interval(start: min(start, other.start), end: max(end, other.end))
        }
    }

    private struct Entry: Codable {
        let fileId: String
        let modifiedTime: String
        let totalSize: Int64
        var intervals: [Interval]
        var lastAccessed: Date
    }

    private let lock = NSLock()
    private var index: [String: Entry] = [:]
    private let dataDir: URL
    private let indexURL: URL

    private static let defaultMaxBytes: Int64 = 20 * 1_073_741_824  // 20 GB
    private static let maxBytesKey = "googleDriveRangeCacheMaxBytes"

    static var maxBytes: Int64 {
        get {
            let v = AppSettings.shared.int64(forKey: maxBytesKey) ?? 0
            return v > 0 ? v : defaultMaxBytes
        }
        set { AppSettings.shared.set(newValue, forKey: maxBytesKey) }
    }

    private init() {
        let base = AppPaths.driveRangesCache
        dataDir = base
        indexURL = AppPaths.driveRangesIndex
        if let data = try? Data(contentsOf: indexURL),
           let entries = try? JSONDecoder().decode([Entry].self, from: data) {
            for e in entries where FileManager.default.fileExists(atPath: base.appendingPathComponent(e.fileId).path) {
                index[e.fileId] = e
            }
        }
    }

    // MARK: - Public API (synchronous — safe to call directly from VFS callbacks)

    /// Returns the requested bytes if fully covered by a cached range, nil on miss.
    func read(fileId: String, modifiedTime: String, offset: Int64, length: Int) -> Data? {
        lock.lock()
        guard let entry = index[fileId], entry.modifiedTime == modifiedTime,
              entry.intervals.contains(where: { $0.covers(offset: offset, length: Int64(length)) }) else {
            lock.unlock()
            return nil
        }
        lock.unlock()

        let file = dataDir.appendingPathComponent(fileId)
        guard let fh = try? FileHandle(forReadingFrom: file) else { return nil }
        defer { try? fh.close() }
        try? fh.seek(toOffset: UInt64(offset))
        let data = fh.readData(ofLength: length)
        guard data.count == length else { return nil }

        lock.lock()
        if var e = index[fileId] { e.lastAccessed = Date(); index[fileId] = e }
        lock.unlock()
        return data
    }

    /// Writes data at the given file offset, updating the in-memory interval index.
    /// Disk persistence and eviction happen on a background task.
    func write(fileId: String, modifiedTime: String, totalSize: Int64, offset: Int64, data: Data) {
        let file = dataDir.appendingPathComponent(fileId)

        // Reset if file changed on Drive.
        lock.lock()
        if let existing = index[fileId], existing.modifiedTime != modifiedTime {
            index.removeValue(forKey: fileId)
            lock.unlock()
            try? FileManager.default.removeItem(at: file)
        } else {
            lock.unlock()
        }

        // Create pre-sized sparse file on first write.
        if !FileManager.default.fileExists(atPath: file.path) {
            FileManager.default.createFile(atPath: file.path, contents: nil)
            if let fh = try? FileHandle(forWritingTo: file) {
                try? fh.truncate(atOffset: UInt64(max(totalSize, 1)))
                try? fh.close()
            }
        }

        // Write the range.
        if let fh = try? FileHandle(forWritingTo: file) {
            defer { try? fh.close() }
            try? fh.seek(toOffset: UInt64(offset))
            fh.write(data)
        }

        // Update in-memory index.
        lock.lock()
        var entry = index[fileId] ?? Entry(fileId: fileId, modifiedTime: modifiedTime,
                                           totalSize: totalSize, intervals: [], lastAccessed: Date())
        entry.intervals.append(Interval(start: offset, end: offset + Int64(data.count)))
        entry.intervals = merge(entry.intervals)
        entry.lastAccessed = Date()
        index[fileId] = entry
        let snapshot = Array(index.values)
        lock.unlock()

        // Persist index and evict in background — never blocks the caller.
        Task.detached { [weak self] in
            guard let self else { return }
            self.persistIndex(snapshot)
            self.evictIfNeeded()
        }
    }

    func invalidate(fileId: String) {
        lock.lock()
        index.removeValue(forKey: fileId)
        let snapshot = Array(index.values)
        lock.unlock()
        try? FileManager.default.removeItem(at: dataDir.appendingPathComponent(fileId))
        persistIndex(snapshot)
    }

    func currentSize() -> Int64 {
        lock.lock()
        let total = index.values.reduce(Int64(0)) { $0 + $1.totalSize }
        lock.unlock()
        return total
    }

    func clear() {
        lock.lock()
        let ids = Array(index.keys)
        index = [:]
        lock.unlock()
        for id in ids { try? FileManager.default.removeItem(at: dataDir.appendingPathComponent(id)) }
        persistIndex([])
    }

    // MARK: - Private

    private func evictIfNeeded() {
        let limit = Self.maxBytes
        lock.lock()
        var total = index.values.reduce(Int64(0)) { $0 + $1.totalSize }
        guard total > limit else { lock.unlock(); return }
        let sorted = index.values.sorted { $0.lastAccessed < $1.lastAccessed }
        var toRemove: [String] = []
        for entry in sorted {
            guard total > limit else { break }
            toRemove.append(entry.fileId)
            total -= entry.totalSize
        }
        for id in toRemove { index.removeValue(forKey: id) }
        let snapshot = Array(index.values)
        lock.unlock()
        for id in toRemove { try? FileManager.default.removeItem(at: dataDir.appendingPathComponent(id)) }
        persistIndex(snapshot)
    }

    private func merge(_ intervals: [Interval]) -> [Interval] {
        let sorted = intervals.sorted { $0.start < $1.start }
        var result: [Interval] = []
        for interval in sorted {
            if let last = result.last, last.canMerge(interval) {
                result[result.count - 1] = last.merged(with: interval)
            } else {
                result.append(interval)
            }
        }
        return result
    }

    private func persistIndex(_ entries: [Entry]) {
        guard let data = try? JSONEncoder().encode(entries) else { return }
        try? data.write(to: indexURL)
    }
}

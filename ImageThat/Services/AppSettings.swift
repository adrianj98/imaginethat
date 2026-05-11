import Foundation

/// File-backed key/value settings store, replacing `UserDefaults.standard`.
///
/// Persisted as a binary plist at `~/.imagethat/settings.plist` so settings
/// live alongside caches and passwords under a single discoverable directory
/// and are not lost when the build directory is wiped.
public final class AppSettings: @unchecked Sendable {
    public static let shared = AppSettings()

    private let fileURL: URL
    private var dict: [String: Any] = [:]
    private let lock = NSLock()

    private init() {
        fileURL = AppPaths.settingsFile
        if let data = try? Data(contentsOf: fileURL),
           let plist = try? PropertyListSerialization.propertyList(from: data, options: [], format: nil) as? [String: Any] {
            dict = plist
        }
    }

    // MARK: - Reads

    public func string(forKey key: String) -> String? {
        lock.lock(); defer { lock.unlock() }
        return dict[key] as? String
    }

    public func bool(forKey key: String) -> Bool {
        lock.lock(); defer { lock.unlock() }
        if let v = dict[key] as? Bool { return v }
        if let v = dict[key] as? NSNumber { return v.boolValue }
        return false
    }

    public func data(forKey key: String) -> Data? {
        lock.lock(); defer { lock.unlock() }
        return dict[key] as? Data
    }

    public func stringArray(forKey key: String) -> [String]? {
        lock.lock(); defer { lock.unlock() }
        return dict[key] as? [String]
    }

    public func object(forKey key: String) -> Any? {
        lock.lock(); defer { lock.unlock() }
        return dict[key]
    }

    public func int64(forKey key: String) -> Int64? {
        lock.lock(); defer { lock.unlock() }
        if let v = dict[key] as? Int64 { return v }
        if let v = dict[key] as? Int { return Int64(v) }
        if let v = dict[key] as? NSNumber { return v.int64Value }
        return nil
    }

    /// Reads a URL stored as a path string (see `setURL(_:forKey:)`).
    public func url(forKey key: String) -> URL? {
        guard let path = string(forKey: key) else { return nil }
        return URL(fileURLWithPath: path)
    }

    // MARK: - Writes

    public func set(_ value: Any?, forKey key: String) {
        lock.lock()
        if let value = value as? Int64 {
            dict[key] = NSNumber(value: value)
        } else if let value {
            dict[key] = value
        } else {
            dict.removeValue(forKey: key)
        }
        lock.unlock()
        save()
    }

    /// Stores a URL as a file-system path string. Reading it back via
    /// `url(forKey:)` returns a `URL(fileURLWithPath:)` of that path.
    public func setURL(_ url: URL?, forKey key: String) {
        set(url?.path, forKey: key)
    }

    public func removeObject(forKey key: String) {
        lock.lock()
        dict.removeValue(forKey: key)
        lock.unlock()
        save()
    }

    // MARK: - Persistence

    private func save() {
        lock.lock()
        let snapshot = dict
        lock.unlock()
        guard let data = try? PropertyListSerialization.data(fromPropertyList: snapshot, format: .binary, options: 0) else { return }
        try? data.write(to: fileURL, options: .atomic)
    }
}

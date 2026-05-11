import AppKit
import CryptoKit
import Foundation

public protocol VaultCryptoBridge {
    func encryptData(_ plaintext: Data) throws -> Data
    func decryptData(_ ciphertext: Data) throws -> Data
    func fileSize(path: String) throws -> Int64
}

extension GocryptfsBridge: VaultCryptoBridge {}
extension DriveVaultBridge: VaultCryptoBridge {}

/// On-disk thumbnail cache. Vault thumbnails are encrypted using the vault's
/// own gocryptfs content encryption — same crypto as the source files.
public final class PersistentThumbnailCache: @unchecked Sendable {
    public static let shared = PersistentThumbnailCache()

    private let cacheRoot: URL

    private init() {
        cacheRoot = AppPaths.thumbnailCache
    }

    // MARK: - Public API

    /// Get a cached thumbnail. Returns nil on miss or if source file changed (size mismatch).
    public func get(vaultPath: String, filePath: String, expectedSize: Int64, bridge: any VaultCryptoBridge) -> NSImage? {
        let fileURL = cacheFileURL(vaultPath: vaultPath, filePath: filePath)
        guard let data = try? Data(contentsOf: fileURL) else { return nil }

        // Minimum: 4 bytes size header + some encrypted data
        guard data.count > 4 else { return nil }

        // Read stored file size (first 4 bytes, big-endian UInt32)
        let storedSize = data.withUnsafeBytes { ptr -> Int64 in
            Int64(UInt32(bigEndian: ptr.load(fromByteOffset: 0, as: UInt32.self)))
        }

        // Invalidate if source file size changed
        guard storedSize == expectedSize else {
            try? FileManager.default.removeItem(at: fileURL)
            return nil
        }

        // Decrypt using vault's encryption
        let encrypted = data.dropFirst(4)
        guard let jpegData = try? bridge.decryptData(Data(encrypted)) else { return nil }
        return NSImage(data: jpegData)
    }

    /// Save a thumbnail encrypted with the vault's encryption.
    public func set(_ image: NSImage, vaultPath: String, filePath: String, fileSize: Int64, bridge: any VaultCryptoBridge) {
        guard let jpegData = jpegData(from: image) else { return }
        guard let encrypted = try? bridge.encryptData(jpegData) else { return }

        // Build cache entry: [4 bytes size] + [encrypted data]
        var entry = Data(capacity: 4 + encrypted.count)
        var sizeBytes = UInt32(min(fileSize, Int64(UInt32.max))).bigEndian
        entry.append(Data(bytes: &sizeBytes, count: 4))
        entry.append(encrypted)

        let fileURL = cacheFileURL(vaultPath: vaultPath, filePath: filePath)
        let dir = fileURL.deletingLastPathComponent()
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        try? entry.write(to: fileURL)
    }

    /// Clear cached thumbnails for a specific vault.
    public func clearVault(path: String) {
        let dir = vaultCacheDir(vaultPath: path)
        try? FileManager.default.removeItem(at: dir)
    }

    /// Clear all cached thumbnails.
    public func clearAll() {
        try? FileManager.default.removeItem(at: cacheRoot)
        try? FileManager.default.createDirectory(at: cacheRoot, withIntermediateDirectories: true)
    }

    /// Size of the cache directory in bytes.
    public func cacheSize() -> Int64 {
        let fm = FileManager.default
        guard let enumerator = fm.enumerator(at: cacheRoot, includingPropertiesForKeys: [.fileSizeKey]) else { return 0 }
        var total: Int64 = 0
        for case let fileURL as URL in enumerator {
            if let size = try? fileURL.resourceValues(forKeys: [.fileSizeKey]).fileSize {
                total += Int64(size)
            }
        }
        return total
    }

    // MARK: - Private

    private func vaultCacheDir(vaultPath: String) -> URL {
        let hash = SHA256.hash(data: Data(vaultPath.utf8))
        let hex = hash.prefix(16).map { String(format: "%02x", $0) }.joined()
        return cacheRoot.appendingPathComponent(hex, isDirectory: true)
    }

    private func cacheFileURL(vaultPath: String, filePath: String) -> URL {
        let dir = vaultCacheDir(vaultPath: vaultPath)
        let fileHash = SHA256.hash(data: Data(filePath.utf8))
        let hex = fileHash.map { String(format: "%02x", $0) }.joined()
        return dir.appendingPathComponent(hex + ".cache")
    }

    private func jpegData(from image: NSImage) -> Data? {
        guard let tiff = image.tiffRepresentation,
              let rep = NSBitmapImageRep(data: tiff) else { return nil }
        return rep.representation(using: .jpeg, properties: [.compressionFactor: 0.8])
    }
}

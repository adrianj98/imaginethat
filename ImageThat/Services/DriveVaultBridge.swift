import Foundation
import CLibGocryptfs

/// A gocryptfs session backed by Google Drive via VFS callbacks.
/// Mirrors GocryptfsBridge but opens the vault from in-memory config bytes
/// and reads encrypted file content through DriveVaultFS callbacks.
public final class DriveVaultBridge: @unchecked Sendable {
    private let sessionID: Int32
    // Kept alive so the C callback pointers remain valid for the session lifetime.
    let fs: DriveVaultFS
    public let folderId: String

    public init(folderId: String,
                service: GoogleDriveService = .shared,
                cache: DriveCache = .shared,
                password: String) async throws {
        self.folderId = folderId
        let vaultFS = DriveVaultFS(rootFolderId: folderId, service: service, cache: cache)
        self.fs = vaultFS

        let configData = try await vaultFS.downloadConfig()
        let ctx = Unmanaged.passUnretained(vaultFS).toOpaque()

        let id: Int32 = configData.withUnsafeBytes { configPtr in
            password.withCString { passPtr in
                GocryptfsOpenVFS(
                    UnsafeMutablePointer(mutating: configPtr.bindMemory(to: Int8.self).baseAddress!),
                    Int32(configData.count),
                    UnsafeMutablePointer(mutating: passPtr),
                    _driveVaultReadAtFn,
                    _driveVaultFileSizeFn,
                    _driveVaultListDirFn,
                    ctx
                )
            }
        }

        switch id {
        case -1: throw GocryptfsError.configNotFound
        case -2: throw GocryptfsError.wrongPassword
        case -3: throw GocryptfsError.cryptoError
        default:
            guard id > 0 else { throw GocryptfsError.cryptoError }
            self.sessionID = id
        }
    }

    deinit {
        GocryptfsClose(sessionID)
    }

    // MARK: - Read operations (same interface as GocryptfsBridge)

    public func listDirectory(path: String = "") throws -> [VaultEntry] {
        let result: ListResult = path.withCString { pathPtr in
            GocryptfsListDir(sessionID, UnsafeMutablePointer(mutating: pathPtr))
        }
        defer { GocryptfsFreeListResult(result) }

        if let errPtr = result.error {
            throw GocryptfsError.listFailed(String(cString: errPtr))
        }
        guard result.count > 0, let entries = result.entries else { return [] }
        return (0..<Int(result.count)).map { i in
            let e = entries[i]
            return VaultEntry(name: String(cString: e.name), isDirectory: e.is_dir != 0, size: e.size)
        }
    }

    public func decryptFile(path: String) throws -> Data {
        let result: DecryptResult = path.withCString { pathPtr in
            GocryptfsDecryptFile(sessionID, UnsafeMutablePointer(mutating: pathPtr))
        }
        defer { GocryptfsFreeDecryptResult(result) }
        if let errPtr = result.error { throw GocryptfsError.decryptFailed(String(cString: errPtr)) }
        guard let dataPtr = result.data, result.size > 0 else { return Data() }
        return Data(bytes: dataPtr, count: Int(result.size))
    }

    public func readRange(path: String, offset: Int64, length: Int32) throws -> Data {
        var buffer = Data(count: Int(length))
        let bytesRead: Int32 = buffer.withUnsafeMutableBytes { bufPtr in
            path.withCString { pathPtr in
                GocryptfsReadRange(
                    sessionID,
                    UnsafeMutablePointer(mutating: pathPtr),
                    offset, length,
                    bufPtr.baseAddress!
                )
            }
        }
        guard bytesRead >= 0 else {
            throw GocryptfsError.decryptFailed("readRange failed at offset=\(offset)")
        }
        return buffer.prefix(Int(bytesRead))
    }

    public func fileSize(path: String) throws -> Int64 {
        let size = path.withCString { ptr in
            GocryptfsFileSize(sessionID, UnsafeMutablePointer(mutating: ptr))
        }
        guard size >= 0 else { throw GocryptfsError.decryptFailed("fileSize failed") }
        return size
    }

    // MARK: - Write helpers

    /// Decrypt ciphertext data using the vault's content encryption.
    public func decryptData(_ ciphertext: Data) throws -> Data {
        let result: DecryptResult = ciphertext.withUnsafeBytes { ptr in
            GocryptfsDecryptData(sessionID, UnsafeMutableRawPointer(mutating: ptr.baseAddress!), Int32(ciphertext.count))
        }
        defer { GocryptfsFreeDecryptResult(result) }
        if let errPtr = result.error { throw GocryptfsError.decryptFailed(String(cString: errPtr)) }
        guard let dataPtr = result.data, result.size > 0 else { return Data() }
        return Data(bytes: dataPtr, count: Int(result.size))
    }

    /// Encrypt plaintext data using the vault's content encryption.
    public func encryptData(_ plaintext: Data) throws -> Data {
        let result: DecryptResult = plaintext.withUnsafeBytes { ptr in
            GocryptfsEncryptData(
                sessionID,
                UnsafeMutableRawPointer(mutating: ptr.baseAddress!),
                Int32(plaintext.count)
            )
        }
        defer { GocryptfsFreeDecryptResult(result) }
        if let errPtr = result.error { throw GocryptfsError.decryptFailed(String(cString: errPtr)) }
        guard let dataPtr = result.data, result.size > 0 else { return Data() }
        return Data(bytes: dataPtr, count: Int(result.size))
    }

    /// Encrypt a plaintext filename for a given cipher directory path.
    /// Returns the encrypted name (used as the Drive file name), or nil on failure.
    public func encryptFileName(cipherDirPath: String, plainName: String) -> String? {
        let ptr = cipherDirPath.withCString { dirPtr in
            plainName.withCString { namePtr in
                GocryptfsEncryptFileName(
                    sessionID,
                    UnsafeMutablePointer(mutating: dirPtr),
                    UnsafeMutablePointer(mutating: namePtr)
                )
            }
        }
        guard let ptr else { return nil }
        let name = String(cString: ptr)
        free(ptr)
        return name
    }

    /// Resolve a vault-relative plaintext path to its cipher equivalent.
    public func cipherPath(forPlainPath plainPath: String, isDir: Bool = false) -> String? {
        let ptr = plainPath.withCString { pathPtr in
            GocryptfsResolveCipherPath(
                sessionID,
                UnsafeMutablePointer(mutating: pathPtr),
                isDir ? 1 : 0
            )
        }
        guard let ptr else { return nil }
        let s = String(cString: ptr)
        free(ptr)
        return s
    }

    /// Returns the Drive file ID for the given vault-relative plaintext path, or nil.
    public func driveFileId(forVaultPath plainPath: String) -> String? {
        guard let cp = cipherPath(forPlainPath: plainPath, isDir: false) else { return nil }
        return fs.getFileId(forCipherPath: cp)
    }

    /// Returns the Drive folder ID for the given vault-relative plaintext directory path, or nil.
    public func driveFolderId(forVaultDir plainDir: String) -> String? {
        guard let cp = cipherPath(forPlainPath: plainDir, isDir: true) else { return nil }
        return fs.getFolderId(forCipherDir: cp)
    }
}

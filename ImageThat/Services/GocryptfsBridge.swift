import Foundation
import CLibGocryptfs

public struct VaultEntry {
    public let name: String
    public let isDirectory: Bool
    public let size: Int64

    public init(name: String, isDirectory: Bool, size: Int64) {
        self.name = name
        self.isDirectory = isDirectory
        self.size = size
    }
}

public enum GocryptfsError: LocalizedError {
    case configNotFound
    case wrongPassword
    case cryptoError
    case sessionInvalid
    case listFailed(String)
    case decryptFailed(String)
    case writeFailed(String)
    case moveFailed(String)
    case mkdirFailed(String)
    case deleteFailed(String)

    public var errorDescription: String? {
        switch self {
        case .configNotFound: return "gocryptfs.conf not found"
        case .wrongPassword: return "Wrong password"
        case .cryptoError: return "Crypto initialization failed"
        case .sessionInvalid: return "Invalid vault session"
        case .listFailed(let msg): return "Failed to list directory: \(msg)"
        case .decryptFailed(let msg): return "Decryption failed: \(msg)"
        case .writeFailed(let msg): return "Failed to write file: \(msg)"
        case .moveFailed(let msg): return "Failed to move file: \(msg)"
        case .mkdirFailed(let msg): return "Failed to create folder: \(msg)"
        case .deleteFailed(let msg): return "Failed to delete: \(msg)"
        }
    }
}

public final class GocryptfsBridge: @unchecked Sendable {
    private let sessionID: Int32
    public let vaultPath: URL

    public init(vaultPath: URL, password: String) throws {
        self.vaultPath = vaultPath

        let id = vaultPath.path.withCString { pathPtr in
            password.withCString { passPtr in
                GocryptfsOpen(
                    UnsafeMutablePointer(mutating: pathPtr),
                    UnsafeMutablePointer(mutating: passPtr)
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

    public func listDirectory(path: String = "") throws -> [VaultEntry] {
        let result: ListResult = path.withCString { pathPtr in
            GocryptfsListDir(sessionID, UnsafeMutablePointer(mutating: pathPtr))
        }
        defer { GocryptfsFreeListResult(result) }

        if let errPtr = result.error {
            let msg = String(cString: errPtr)
            throw GocryptfsError.listFailed(msg)
        }

        guard result.count > 0, let entries = result.entries else {
            return []
        }

        var items: [VaultEntry] = []
        for i in 0..<Int(result.count) {
            let entry = entries[i]
            let name = String(cString: entry.name)
            items.append(VaultEntry(
                name: name,
                isDirectory: entry.is_dir != 0,
                size: entry.size
            ))
        }
        return items
    }

    public func decryptFile(path: String) throws -> Data {
        let result: DecryptResult = path.withCString { pathPtr in
            GocryptfsDecryptFile(sessionID, UnsafeMutablePointer(mutating: pathPtr))
        }
        defer { GocryptfsFreeDecryptResult(result) }

        if let errPtr = result.error {
            let msg = String(cString: errPtr)
            throw GocryptfsError.decryptFailed(msg)
        }

        guard let dataPtr = result.data, result.size > 0 else {
            return Data()
        }

        return Data(bytes: dataPtr, count: Int(result.size))
    }

    public func readRange(path: String, offset: Int64, length: Int32) throws -> Data {
        var buffer = Data(count: Int(length))
        let bytesRead = buffer.withUnsafeMutableBytes { bufPtr -> Int32 in
            path.withCString { pathPtr in
                GocryptfsReadRange(
                    sessionID,
                    UnsafeMutablePointer(mutating: pathPtr),
                    offset,
                    length,
                    bufPtr.baseAddress!
                )
            }
        }

        if bytesRead < 0 {
            NSLog("[GocryptfsBridge] readRange FAILED: path=%@, offset=%lld, length=%d, bytesRead=%d",
                  path, offset, length, bytesRead)
            throw GocryptfsError.decryptFailed("readRange failed at offset=\(offset) length=\(length)")
        }

        if bytesRead == 0 {
            // Zero bytes read - could be EOF or error
            NSLog("[GocryptfsBridge] readRange EOF: path=%@, offset=%lld, length=%d",
                  path, offset, length)
        }

        return buffer.prefix(Int(bytesRead))
    }

    public func fileSize(path: String) throws -> Int64 {
        let size = path.withCString { pathPtr in
            GocryptfsFileSize(sessionID, UnsafeMutablePointer(mutating: pathPtr))
        }
        guard size >= 0 else {
            throw GocryptfsError.decryptFailed("fileSize failed")
        }
        return size
    }

    /// Encrypt data using the vault's gocryptfs content encryption.
    public func encryptData(_ plaintext: Data) throws -> Data {
        let result: DecryptResult = plaintext.withUnsafeBytes { ptr in
            GocryptfsEncryptData(sessionID, UnsafeMutableRawPointer(mutating: ptr.baseAddress!), Int32(plaintext.count))
        }
        defer { GocryptfsFreeDecryptResult(result) }

        if let errPtr = result.error {
            throw GocryptfsError.decryptFailed(String(cString: errPtr))
        }
        guard let dataPtr = result.data, result.size > 0 else { return Data() }
        return Data(bytes: dataPtr, count: Int(result.size))
    }

    /// Decrypt data using the vault's gocryptfs content encryption.
    public func decryptData(_ ciphertext: Data) throws -> Data {
        let result: DecryptResult = ciphertext.withUnsafeBytes { ptr in
            GocryptfsDecryptData(sessionID, UnsafeMutableRawPointer(mutating: ptr.baseAddress!), Int32(ciphertext.count))
        }
        defer { GocryptfsFreeDecryptResult(result) }

        if let errPtr = result.error {
            throw GocryptfsError.decryptFailed(String(cString: errPtr))
        }
        guard let dataPtr = result.data, result.size > 0 else { return Data() }
        return Data(bytes: dataPtr, count: Int(result.size))
    }

    /// Move a file from one directory to another within the vault.
    public func moveFile(fromPath: String, toDirectory: String) throws {
        let result = fromPath.withCString { srcPtr in
            toDirectory.withCString { dstPtr in
                GocryptfsRenameFile(
                    sessionID,
                    UnsafeMutablePointer(mutating: srcPtr),
                    UnsafeMutablePointer(mutating: dstPtr)
                )
            }
        }
        guard result == 0 else {
            throw GocryptfsError.moveFailed("error code \(result)")
        }
    }

    /// Create a new directory in the vault.
    public func mkdir(path: String) throws {
        let result = path.withCString { ptr in
            GocryptfsMkdir(sessionID, UnsafeMutablePointer(mutating: ptr))
        }
        guard result == 0 else {
            throw GocryptfsError.mkdirFailed("error code \(result)")
        }
    }

    /// Delete a file from the vault.
    public func deleteFile(path: String) throws {
        let result = path.withCString { ptr in
            GocryptfsDeleteFile(sessionID, UnsafeMutablePointer(mutating: ptr))
        }
        guard result == 0 else {
            throw GocryptfsError.deleteFailed("error code \(result)")
        }
    }

    /// Delete a directory and all its contents from the vault.
    public func deleteDirectory(path: String) throws {
        let result = path.withCString { ptr in
            GocryptfsDeleteDir(sessionID, UnsafeMutablePointer(mutating: ptr))
        }
        guard result == 0 else {
            throw GocryptfsError.deleteFailed("error code \(result)")
        }
    }

    /// Write encrypted file data to the vault. Creates parent directories and handles long-name sidecars.
    public func writeFile(path: String, data: Data) throws {
        let result = data.withUnsafeBytes { ptr in
            path.withCString { pathPtr in
                GocryptfsWriteFile(
                    sessionID,
                    UnsafeMutablePointer(mutating: pathPtr),
                    UnsafeMutableRawPointer(mutating: ptr.baseAddress!),
                    Int32(data.count)
                )
            }
        }
        guard result == 0 else {
            throw GocryptfsError.writeFailed("error code \(result)")
        }
    }
}

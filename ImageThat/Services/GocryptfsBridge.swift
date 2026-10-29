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
    let sessionID: Int32
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

    /// Wraps a session another open export created (see GocryptfsTransfer.swift).
    init(sessionID: Int32, vaultPath: URL) {
        self.sessionID = sessionID
        self.vaultPath = vaultPath
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

    /// Describes a non-zero return code of the write exports (see libgocryptfs.h).
    static func describeWriteCode(_ rc: Int32) -> String {
        switch rc {
        case -1: return "vault is locked or read-only"
        case -2: return "not found"
        case -3: return "invalid path"
        case -4: return "already exists"
        case -5: return "storage error"
        default: return "error code \(rc)"
        }
    }

    /// Replace (or create) a file from in-memory data. The parent directory must
    /// exist. For anything large use `createFile(path:)`, which streams.
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
            throw GocryptfsError.writeFailed(Self.describeWriteCode(result))
        }
    }


    // MARK: - Thumbnails (core/thumb through the engine)

    /// Set up the preview caches for this session: an encrypted memory tier
    /// and, when `diskDir` is given, a persistent tier sealed with the vault's
    /// own key. `sourceID` must be stable across launches (the vault path).
    public func configureThumbnails(sourceID: String, diskDir: URL?, memBytes: Int64 = 0, diskBytes: Int64 = 0) throws {
        try GocryptfsThumbnails.configure(sessionID: sessionID, sourceID: sourceID, diskDir: diskDir,
                                          memBytes: memBytes, diskBytes: diskBytes)
    }

    /// A JPEG preview from the caches or the engine's decoders, or nil when the
    /// engine cannot decode this file (HEIC, RAW, video): render it on the host
    /// and hand it to `storeThumbnail` so the caches learn it.
    public func thumbnail(path: String, maxEdge: Int) throws -> Data? {
        try GocryptfsThumbnails.get(sessionID: sessionID, path: path, maxEdge: maxEdge)
    }

    /// Store a preview the host rendered itself.
    public func storeThumbnail(_ jpeg: Data, path: String, maxEdge: Int) throws {
        try GocryptfsThumbnails.put(sessionID: sessionID, jpeg: jpeg, path: path, maxEdge: maxEdge)
    }

    /// Drop every cached preview of a path after it changed.
    public func invalidateThumbnail(path: String) {
        GocryptfsThumbnails.invalidate(sessionID: sessionID, path: path)
    }

    /// Start streaming a new file into the vault. The entry appears when the
    /// returned writer is finished; see `VaultFileWriter`.
    public func createFile(path: String) throws -> VaultFileWriter {
        let id = path.withCString { ptr in
            GocryptfsCreateFile(sessionID, UnsafeMutablePointer(mutating: ptr))
        }
        guard id > 0 else {
            throw GocryptfsError.writeFailed(Self.describeWriteCode(id))
        }
        return VaultFileWriter(writerID: id, path: path)
    }
}

extension GocryptfsBridge: VaultThumbnailing {}

/// Preview caching and rendering a vault session offers through the engine.
/// Both local (`GocryptfsBridge`) and Drive (`DriveVaultBridge`) sessions
/// provide it, so `AppState` treats them alike.
public protocol VaultThumbnailing: AnyObject {
    func configureThumbnails(sourceID: String, diskDir: URL?, memBytes: Int64, diskBytes: Int64) throws
    func thumbnail(path: String, maxEdge: Int) throws -> Data?
    func storeThumbnail(_ jpeg: Data, path: String, maxEdge: Int) throws
    func invalidateThumbnail(path: String)
}

/// The thumbnail exports of libgocryptfs, shared by the two bridge classes.
public enum GocryptfsThumbnails {
    static func configure(sessionID: Int32, sourceID: String, diskDir: URL?, memBytes: Int64, diskBytes: Int64) throws {
        let dir = diskDir?.path ?? ""
        let rc = sourceID.withCString { srcPtr in
            dir.withCString { dirPtr in
                GocryptfsThumbConfigure(sessionID, UnsafeMutablePointer(mutating: srcPtr),
                                        UnsafeMutablePointer(mutating: dirPtr), memBytes, diskBytes)
            }
        }
        guard rc == 0 else { throw GocryptfsError.writeFailed("thumbnail cache: " + GocryptfsBridge.describeWriteCode(rc)) }
    }

    static func get(sessionID: Int32, path: String, maxEdge: Int) throws -> Data? {
        let result: DecryptResult = path.withCString { pathPtr in
            GocryptfsThumbGet(sessionID, UnsafeMutablePointer(mutating: pathPtr), Int32(maxEdge))
        }
        defer { GocryptfsFreeDecryptResult(result) }
        if let errPtr = result.error { throw GocryptfsError.decryptFailed(String(cString: errPtr)) }
        guard let dataPtr = result.data, result.size > 0 else { return nil }
        return Data(bytes: dataPtr, count: Int(result.size))
    }

    static func put(sessionID: Int32, jpeg: Data, path: String, maxEdge: Int) throws {
        guard !jpeg.isEmpty else { return }
        let rc = jpeg.withUnsafeBytes { ptr in
            path.withCString { pathPtr in
                GocryptfsThumbPut(sessionID, UnsafeMutablePointer(mutating: pathPtr), Int32(maxEdge),
                                  UnsafeMutableRawPointer(mutating: ptr.baseAddress!), Int32(jpeg.count))
            }
        }
        guard rc == 0 else { throw GocryptfsError.writeFailed("thumbnail cache: " + GocryptfsBridge.describeWriteCode(rc)) }
    }

    static func invalidate(sessionID: Int32, path: String) {
        path.withCString { pathPtr in
            GocryptfsThumbInvalidate(sessionID, UnsafeMutablePointer(mutating: pathPtr))
        }
    }

    /// Delete every persisted preview under `diskDir`, for every vault.
    public static func clearCache(at diskDir: URL) throws {
        let rc = diskDir.path.withCString { ptr in
            GocryptfsThumbCacheClear(UnsafeMutablePointer(mutating: ptr))
        }
        guard rc == 0 else { throw GocryptfsError.deleteFailed("thumbnail cache: " + GocryptfsBridge.describeWriteCode(rc)) }
    }

    /// Bytes persisted under `diskDir`.
    public static func cacheSize(at diskDir: URL) -> Int64 {
        diskDir.path.withCString { ptr in
            GocryptfsThumbCacheSize(UnsafeMutablePointer(mutating: ptr))
        }
    }
}

/// Streams plaintext into a new vault file with bounded memory.
///
/// The Go engine encrypts each chunk as it arrives into a temporary ciphertext
/// file and renames it into place on `finish()`. Nothing decrypted is written
/// anywhere. `abort()`, or releasing the writer without finishing, discards the
/// file and leaves no entry and no temp file behind.
public final class VaultFileWriter: @unchecked Sendable {
    private let writerID: Int32
    public let path: String
    private var ended = false

    fileprivate init(writerID: Int32, path: String) {
        self.writerID = writerID
        self.path = path
    }

    deinit {
        abort()
    }

    /// Append a chunk. Blocks until the engine has encrypted it.
    public func write(_ chunk: Data) throws {
        guard !ended else { throw GocryptfsError.writeFailed("writer already finished") }
        var offset = 0
        while offset < chunk.count {
            let n = min(chunk.count - offset, Int(Int32.max))
            let rc = chunk.withUnsafeBytes { ptr -> Int32 in
                GocryptfsWriterWrite(
                    writerID,
                    UnsafeMutableRawPointer(mutating: ptr.baseAddress!.advanced(by: offset)),
                    Int32(n)
                )
            }
            guard rc == 0 else { throw GocryptfsError.writeFailed(GocryptfsBridge.describeWriteCode(rc)) }
            offset += n
        }
    }

    /// Commit the file. After this the writer cannot be used.
    public func finish() throws {
        guard !ended else { return }
        ended = true
        let rc = GocryptfsWriterClose(writerID)
        guard rc == 0 else { throw GocryptfsError.writeFailed(GocryptfsBridge.describeWriteCode(rc)) }
    }

    /// Discard the file. Safe to call after `finish()` (no-op).
    public func abort() {
        guard !ended else { return }
        ended = true
        GocryptfsWriterAbort(writerID)
    }
}

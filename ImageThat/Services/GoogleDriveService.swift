import Foundation
import AppKit
import CLibGocryptfs

public enum GoogleDriveError: LocalizedError {
    case notConnected
    case authFailed(String)
    case authCancelled
    case invalidResponse
    case notFound(String)
    case requestFailed(String)

    public var errorDescription: String? {
        switch self {
        case .notConnected: return "Not connected to Google Drive"
        case .authFailed(let msg): return "Google Drive authentication failed: \(msg)"
        case .authCancelled: return "Connection cancelled"
        case .invalidResponse: return "Invalid response from Google Drive"
        case .notFound(let msg): return "Not found on Google Drive: \(msg)"
        case .requestFailed(let msg): return "Google Drive: \(msg)"
        }
    }

    /// An engine error string, which starts with a code shared with the
    /// Android app ("not_found: ...", "auth: ...").
    init(engineMessage message: String) {
        let parts = message.split(separator: ":", maxSplits: 1)
        let code = parts.count == 2 ? String(parts[0]) : ""
        let text = parts.count == 2 ? parts[1].trimmingCharacters(in: .whitespaces) : message
        switch code {
        case "not_connected": self = .notConnected
        case "auth": self = .authFailed(text)
        case "cancelled": self = .authCancelled
        case "not_found": self = .notFound(text)
        default: self = .requestFailed(text)
        }
    }

    /// A negative return code of the engine's Drive calls (see libgocryptfs.h).
    init(code rc: Int32) {
        switch rc {
        case -1: self = .notFound("no such file or folder")
        case -4: self = .requestFailed("already exists")
        case -7: self = .authCancelled
        case -9: self = .authFailed("access was refused; connect Google Drive again")
        default: self = .requestFailed("the request failed (code \(rc))")
        }
    }
}

public struct DriveFile: Sendable {
    public let id: String
    public let name: String
    public let mimeType: String
    public let size: Int64
    public let thumbnailLink: String?
    public let modifiedTime: String
    public let isFolder: Bool
}

public struct DriveFolder: Sendable {
    public let id: String
    public let name: String
}

// MARK: - Google Drive Service

/// The user's Google Drive account, through the Go engine (core/drive): the
/// same code the Android app runs for sign-in, listing, uploads and Drive
/// vaults. The account is an engine handle made from the OAuth client ID and
/// secret and the refresh token stored in AppSettings.
///
/// Engine calls block on the network, so each runs off the caller's thread.
public final class GoogleDriveService: @unchecked Sendable {
    public static let shared = GoogleDriveService()

    private let lock = NSLock()
    /// The signed-in account's handle, 0 when signed out.
    private var account: Int32 = 0
    /// A handle whose sign-in waits for the browser, 0 when none does.
    private var signingIn: Int32 = 0

    private static let udClientId     = "gdrive.clientId"
    private static let udClientSecret = "gdrive.clientSecret"
    private static let udRefreshToken = "gdrive.refreshToken"

    private init() {}

    // MARK: - Connection state

    public var isConnected: Bool { lock.withLock { account > 0 } }

    /// Opens the stored account, if there is one.
    public func loadSavedCredentials() {
        let s = AppSettings.shared
        guard let clientId = s.string(forKey: Self.udClientId),
              let clientSecret = s.string(forKey: Self.udClientSecret),
              let refreshToken = s.string(forKey: Self.udRefreshToken), !refreshToken.isEmpty else { return }
        replaceAccount(with: Self.newHandle(clientId: clientId, clientSecret: clientSecret, refreshToken: refreshToken))
    }

    public func disconnect() {
        replaceAccount(with: 0, signOut: true)
        let s = AppSettings.shared
        s.removeObject(forKey: Self.udClientId)
        s.removeObject(forKey: Self.udClientSecret)
        s.removeObject(forKey: Self.udRefreshToken)
    }

    private func replaceAccount(with handle: Int32, signOut: Bool = false) {
        let old = lock.withLock { () -> Int32 in
            let old = account
            account = handle
            return old
        }
        guard old > 0 else { return }
        if signOut { GocryptfsDriveSignOut(old) }
        GocryptfsDriveFree(old)
    }

    private static func newHandle(clientId: String, clientSecret: String, refreshToken: String) -> Int32 {
        clientId.withCString { id in
            clientSecret.withCString { secret in
                refreshToken.withCString { token in
                    GocryptfsDriveNew(UnsafeMutablePointer(mutating: id), UnsafeMutablePointer(mutating: secret),
                                      UnsafeMutablePointer(mutating: token))
                }
            }
        }
    }

    // MARK: - OAuth

    /// Signs in in the system browser. The engine listens on a loopback port
    /// for Google's redirect and exchanges the code; the refresh token it
    /// returns is stored like before.
    public func authorize(clientId: String, clientSecret: String) async throws {
        let s = AppSettings.shared
        s.set(clientId, forKey: Self.udClientId)
        s.set(clientSecret, forKey: Self.udClientSecret)

        let handle = Self.newHandle(clientId: clientId, clientSecret: clientSecret, refreshToken: "")
        lock.withLock { signingIn = handle }
        defer { lock.withLock { if signingIn == handle { signingIn = 0 } } }

        guard let urlPtr = GocryptfsDriveSignInStart(handle) else {
            GocryptfsDriveFree(handle)
            throw GoogleDriveError.authFailed("Could not start the sign-in")
        }
        let urlString = String(cString: urlPtr)
        free(urlPtr)
        guard let authURL = URL(string: urlString) else {
            GocryptfsDriveFree(handle)
            throw GoogleDriveError.authFailed("Invalid auth URL")
        }
        await MainActor.run { _ = NSWorkspace.shared.open(authURL) }

        let rc = await Task.detached { GocryptfsDriveSignInWait(handle) }.value
        guard rc == 0 else {
            GocryptfsDriveFree(handle)
            throw GoogleDriveError(code: rc)
        }
        let tokenPtr = GocryptfsDriveRefreshToken(handle)
        let refreshToken = tokenPtr.map { String(cString: $0) } ?? ""
        free(tokenPtr)
        guard !refreshToken.isEmpty else {
            GocryptfsDriveFree(handle)
            throw GoogleDriveError.authFailed("Google returned no refresh token")
        }
        s.set(refreshToken, forKey: Self.udRefreshToken)
        replaceAccount(with: handle)
    }

    /// Ends an `authorize` that waits for the browser; it throws `authCancelled`.
    public func cancelAuthorization() {
        lock.withLock {
            if signingIn > 0 { GocryptfsDriveSignInCancel(signingIn) }
        }
    }

    // MARK: - Engine plumbing

    /// Runs a blocking engine call with the account handle, off the caller's thread.
    private func withAccount<T: Sendable>(_ body: @escaping @Sendable (Int32) throws -> T) async throws -> T {
        let handle = lock.withLock { account }
        guard handle > 0 else { throw GoogleDriveError.notConnected }
        return try await Task.detached { try body(handle) }.value
    }

    /// The bytes of an engine result, or the error it carries. Frees it.
    private static func take(_ result: DecryptResult) throws -> Data {
        defer { GocryptfsFreeDecryptResult(result) }
        if let errPtr = result.error {
            throw GoogleDriveError(engineMessage: String(cString: errPtr))
        }
        guard let dataPtr = result.data, result.size > 0 else { return Data() }
        return Data(bytes: dataPtr, count: Int(result.size))
    }

    private static func check(_ rc: Int32) throws {
        guard rc == 0 else { throw GoogleDriveError(code: rc) }
    }

    /// A Drive file as the engine reports it. Size comes as a string, the way
    /// the Drive API sends it.
    private struct EngineFile: Decodable {
        let id: String
        let name: String
        let mimeType: String?
        let size: String?
        let modifiedTime: String?
        let thumbnailLink: String?
        let isFolder: Bool?

        var file: DriveFile {
            DriveFile(id: id, name: name, mimeType: mimeType ?? "", size: Int64(size ?? "0") ?? 0,
                      thumbnailLink: thumbnailLink, modifiedTime: modifiedTime ?? "", isFolder: isFolder ?? false)
        }
    }

    private static func decode<T: Decodable>(_ type: T.Type, _ data: Data) throws -> T {
        do {
            return try JSONDecoder().decode(type, from: data)
        } catch {
            throw GoogleDriveError.invalidResponse
        }
    }

    // MARK: - Drive API: reads

    public struct VaultRootMatch: Sendable {
        public let rootFolderId: String
        /// Folder names from the vault root down to (and including) the originally
        /// requested descendant. Empty if the descendant *is* the vault root.
        public let descendantPath: [String]
    }

    /// The vault `folderId` is, or lies inside: the nearest folder at or above
    /// it holding gocryptfs.conf, with the folder names from there down to
    /// `folderId`. Nil when there is none (the search stops at My Drive).
    public func findVaultRoot(forDescendant folderId: String) async -> VaultRootMatch? {
        struct Root: Decodable {
            let rootId: String
            let path: [String]?
        }
        let data = try? await withAccount { handle in
            try folderId.withCString { f in
                try Self.take(GocryptfsDriveFindVaultRoot(handle, UnsafeMutablePointer(mutating: f)))
            }
        }
        guard let data, let root = try? Self.decode(Root.self, data) else { return nil }
        return VaultRootMatch(rootFolderId: root.rootId, descendantPath: root.path ?? [])
    }

    /// A folder's entries ("root" is My Drive). `mediaOnly` keeps folders,
    /// images and videos, folders first.
    public func list(in folderId: String, mediaOnly: Bool) async throws -> [DriveFile] {
        let data = try await withAccount { handle in
            try folderId.withCString { f in
                try Self.take(GocryptfsDriveList(handle, UnsafeMutablePointer(mutating: f), mediaOnly ? 1 : 0))
            }
        }
        return try Self.decode([EngineFile].self, data).map(\.file)
    }

    /// Images and videos in a folder.
    public func listFiles(in folderId: String) async throws -> [DriveFile] {
        try await list(in: folderId, mediaOnly: true).filter { !$0.isFolder }
    }

    public func listFolders(in folderId: String) async throws -> [DriveFolder] {
        try await list(in: folderId, mediaOnly: true)
            .filter(\.isFolder)
            .map { DriveFolder(id: $0.id, name: $0.name) }
    }

    /// Up to `length` bytes at `offset` of a plain file; fewer at its end.
    public func readRange(fileId: String, offset: Int64, length: Int) async throws -> Data {
        try await withAccount { handle in
            try fileId.withCString { f in
                try Self.take(GocryptfsDriveReadRange(handle, UnsafeMutablePointer(mutating: f), offset, Int64(length)))
            }
        }
    }

    /// A whole plain file. Drive answers a range past the end with what
    /// there is, so one large range reads all of it.
    public func downloadData(fileId: String) async throws -> Data {
        try await readRange(fileId: fileId, offset: 0, length: 1 << 40)
    }

    /// Drive's own preview of a plain file, at most `maxEdge` pixels.
    public func downloadThumbnail(urlString: String, maxEdge: Int) async throws -> Data {
        try await withAccount { handle in
            try urlString.withCString { l in
                try Self.take(GocryptfsDriveThumbnail(handle, UnsafeMutablePointer(mutating: l), Int32(maxEdge)))
            }
        }
    }

    // MARK: - Drive API: writes

    public func uploadFile(name: String, parentId: String, data: Data, mimeType: String) async throws -> DriveFile {
        try await upload(parentId: parentId, name: name, mimeType: mimeType, updateFileId: "", data: data)
    }

    public func updateFile(fileId: String, data: Data, mimeType: String) async throws {
        _ = try await upload(parentId: "", name: "", mimeType: mimeType, updateFileId: fileId, data: data)
    }

    private func upload(parentId: String, name: String, mimeType: String, updateFileId: String,
                        data: Data) async throws -> DriveFile {
        let result = try await withAccount { handle in
            try data.withUnsafeBytes { buf in
                try parentId.withCString { p in
                    try name.withCString { n in
                        try mimeType.withCString { m in
                            try updateFileId.withCString { u in
                                try Self.take(GocryptfsDriveUpload(
                                    handle, UnsafeMutablePointer(mutating: p), UnsafeMutablePointer(mutating: n),
                                    UnsafeMutablePointer(mutating: m), UnsafeMutablePointer(mutating: u),
                                    UnsafeMutableRawPointer(mutating: buf.baseAddress), Int64(data.count)))
                            }
                        }
                    }
                }
            }
        }
        return try Self.decode(EngineFile.self, result).file
    }

    /// Deletes a file, or a folder with everything in it.
    public func deleteFile(fileId: String) async throws {
        try await withAccount { handle in
            try fileId.withCString { f in try Self.check(GocryptfsDriveDelete(handle, UnsafeMutablePointer(mutating: f))) }
        }
    }

    public func moveFile(fileId: String, from oldParentId: String, to newParentId: String) async throws {
        try await withAccount { handle in
            try fileId.withCString { f in
                try oldParentId.withCString { o in
                    try newParentId.withCString { n in
                        try "".withCString { keepName in
                            try Self.check(GocryptfsDriveMove(handle, UnsafeMutablePointer(mutating: f),
                                                              UnsafeMutablePointer(mutating: o), UnsafeMutablePointer(mutating: n),
                                                              UnsafeMutablePointer(mutating: keepName)))
                        }
                    }
                }
            }
        }
    }

    public func createFolder(name: String, parentId: String) async throws -> DriveFolder {
        let data = try await withAccount { handle in
            try parentId.withCString { p in
                try name.withCString { n in
                    try Self.take(GocryptfsDriveCreateFolder(handle, UnsafeMutablePointer(mutating: p),
                                                             UnsafeMutablePointer(mutating: n)))
                }
            }
        }
        let folder = try Self.decode(EngineFile.self, data)
        return DriveFolder(id: folder.id, name: folder.name)
    }

    // MARK: - Drive vaults

    /// Unlocks the gocryptfs vault in Drive folder `folderId`. The result is
    /// an ordinary writable engine session, like a local vault's: the engine
    /// does every gocryptfs read and write on Drive itself, and keeps the
    /// ciphertext it downloads in the shared Drive cache. Blocks on the
    /// network and scrypt, off the caller's thread.
    public func openVault(folderId: String, password: String) async throws -> GocryptfsBridge {
        GocryptfsDriveCache.applyLimit()
        let cacheDir = GocryptfsDriveCache.directory.path
        let id = try await withAccount { handle in
            folderId.withCString { f in
                password.withCString { p in
                    cacheDir.withCString { d in
                        GocryptfsOpenDrive(handle, UnsafeMutablePointer(mutating: f), UnsafeMutablePointer(mutating: p),
                                           UnsafeMutablePointer(mutating: d))
                    }
                }
            }
        }
        switch id {
        case -1: throw GocryptfsError.configNotFound
        case -2: throw GocryptfsError.wrongPassword
        case let id where id > 0: return GocryptfsBridge(sessionID: id, vaultPath: URL(string: "gdrive://\(folderId)")!)
        default: throw GoogleDriveError(code: id)
        }
    }
}

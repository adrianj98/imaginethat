import Foundation

/// A vault advertised by an imagethat-web server.
public struct RemoteVault: Decodable, Sendable, Hashable {
    public let id: String
    public let name: String
    public let unlocked: Bool
}

/// One entry of a decrypted directory listing from the server.
public struct RemoteEntry: Decodable, Sendable {
    public let name: String
    public let isDir: Bool
    public let size: Int64
}

public enum RemoteServerError: LocalizedError {
    case badResponse
    case invalidServerURL
    case wrongPassword
    case accessPasswordRequired
    case wrongAccessPassword
    case readOnly
    case vaultLocked
    case http(status: Int, message: String)

    public var errorDescription: String? {
        switch self {
        case .badResponse: return "The server sent an unexpected response"
        case .invalidServerURL: return "Enter a server address like https://nas.local:8347"
        case .wrongPassword: return "Wrong password"
        case .accessPasswordRequired: return "This server needs its access password"
        case .wrongAccessPassword: return "Wrong server access password"
        case .readOnly: return "Vaults on this server are decrypted on this Mac and are read-only for now"
        case .vaultLocked: return "The vault is locked"
        case .http(_, let message): return "Server: \(message)"
        }
    }
}

/// How an imagethat-web server works (its -mode flag).
public enum RemoteServerMode: Sendable {
    /// The server unlocks vaults and sends plaintext.
    case serverDecrypt
    /// The server sends only ciphertext behind an access password; vaults are
    /// unlocked and decrypted on this Mac.
    case clientDecrypt
}

/// Talks to an imagethat-web server (webserver/ in this repo).
///
/// A server-decrypt server decrypts; this client only ever sees plaintext in
/// memory. The URLSession is ephemeral with no URL cache, so neither responses
/// nor the session cookie that holds the unlocked vaults reach the disk.
///
/// A client-decrypt server never sees a vault password: the client logs in
/// with the server's access password, and `unlock` opens the vault here
/// through the Go library (GocryptfsBridge over GocryptfsOpenRemote), which
/// fetches ciphertext and decrypts it in this process. Reads then go to that
/// session; writes are refused.
public final class RemoteServerClient: @unchecked Sendable {
    public let baseURL: URL
    private let session: URLSession

    private let lock = NSLock()
    // vault ID → password, in memory only. The server locks idle sessions
    // (default 30 min); with this the client re-unlocks instead of failing.
    private var passwords: [String: String] = [:]
    // Client-decrypt servers: the access password (memory only, for logging
    // in again after the server's idle timeout) and the open vaults.
    private var mode: RemoteServerMode?
    private var accessPassword: String?
    private var bridges: [String: GocryptfsBridge] = [:]

    public init(baseURL: URL) {
        self.baseURL = baseURL
        let config = URLSessionConfiguration.ephemeral
        config.urlCache = nil
        config.requestCachePolicy = .reloadIgnoringLocalCacheData
        config.httpShouldSetCookies = true
        config.httpCookieAcceptPolicy = .always
        config.timeoutIntervalForRequest = 60
        self.session = URLSession(configuration: config)
    }

    deinit {
        session.invalidateAndCancel()
    }

    // MARK: - Mode and login

    /// Asks the server how it works. Servers older than the mode switch have
    /// no /api/info and decrypt themselves.
    public func serverMode() async throws -> RemoteServerMode {
        if let mode = lock.withLock({ self.mode }) { return mode }
        struct Info: Decodable { let mode: String? }
        let (data, response) = try await raw(request("api/info"))
        let mode: RemoteServerMode
        switch response.statusCode {
        case 404:
            mode = .serverDecrypt
        case 200:
            mode = (try? JSONDecoder().decode(Info.self, from: data))?.mode == "client-decrypt"
                ? .clientDecrypt : .serverDecrypt
        default:
            try check(response, data)
            throw RemoteServerError.badResponse
        }
        lock.withLock { self.mode = mode }
        return mode
    }

    public var isClientDecrypt: Bool {
        lock.withLock { mode == .clientDecrypt }
    }

    public var hasAccessPassword: Bool {
        lock.withLock { accessPassword != nil }
    }

    /// Logs in to a client-decrypt server with its access password.
    public func login(accessPassword: String) async throws {
        var req = request("api/login", method: "POST")
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try JSONEncoder().encode(["password": accessPassword])
        let (data, response) = try await raw(req)
        if response.statusCode == 401 { throw RemoteServerError.wrongAccessPassword }
        try check(response, data)
        lock.withLock { self.accessPassword = accessPassword }
    }

    // MARK: - Vaults

    public func listVaults() async throws -> [RemoteVault] {
        struct Response: Decodable { let vaults: [RemoteVault]? }
        var (data, response) = try await raw(request("api/vaults"))
        if response.statusCode == 401, isClientDecrypt {
            guard let access = lock.withLock({ accessPassword }) else { throw RemoteServerError.accessPasswordRequired }
            try await login(accessPassword: access)
            (data, response) = try await raw(request("api/vaults"))
        }
        try check(response, data)
        let vaults = try decode(Response.self, data).vaults ?? []
        guard isClientDecrypt else { return vaults }
        // The server holds nothing open here; this client does.
        let open = lock.withLock { Set(bridges.keys) }
        return vaults.map { RemoteVault(id: $0.id, name: $0.name, unlocked: open.contains($0.id)) }
    }

    public func unlock(vaultID: String, password: String) async throws {
        if try await serverMode() == .clientDecrypt {
            try await unlockLocally(vaultID: vaultID, password: password)
            return
        }
        var req = request("api/vaults/\(vaultID)/unlock", method: "POST")
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try JSONEncoder().encode(["password": password])
        let (data, response) = try await session.data(for: req)
        let http = try httpResponse(response)
        if http.statusCode == 401 { throw RemoteServerError.wrongPassword }
        try check(http, data)
        lock.withLock { passwords[vaultID] = password }
    }

    /// Client-decrypt: opens the vault in this process. The Go library logs
    /// in on its own connection with the access password, so the vault
    /// password goes only to scrypt here.
    private func unlockLocally(vaultID: String, password: String) async throws {
        guard let access = lock.withLock({ accessPassword }) else { throw RemoteServerError.accessPasswordRequired }
        let server = baseURL
        do {
            let bridge = try await Task.detached {
                try GocryptfsBridge(server: server, vaultID: vaultID, password: password, accessPassword: access)
            }.value
            lock.withLock { bridges[vaultID] = bridge }
        } catch GocryptfsError.wrongPassword {
            throw RemoteServerError.wrongPassword
        } catch GocryptfsError.wrongAccessPassword {
            lock.withLock { accessPassword = nil }
            throw RemoteServerError.wrongAccessPassword
        }
    }

    public func lock(vaultID: String) async throws {
        lock.withLock {
            passwords[vaultID] = nil
            bridges[vaultID] = nil
        }
        if isClientDecrypt { return }
        _ = try await send(request("api/vaults/\(vaultID)/lock", method: "POST"))
    }

    /// Ends this client's server session, locking every vault it opened.
    public func logout() async throws {
        lock.withLock {
            passwords.removeAll()
            bridges.removeAll()
            accessPassword = nil
        }
        _ = try await send(request("api/logout", method: "POST"))
    }

    /// The local session of a vault on a client-decrypt server, or nil on a
    /// server-decrypt server (which decrypts itself).
    private func localVault(_ vaultID: String) throws -> GocryptfsBridge? {
        try lock.withLock {
            guard mode == .clientDecrypt else { return nil }
            guard let bridge = bridges[vaultID] else { throw RemoteServerError.vaultLocked }
            return bridge
        }
    }

    /// Runs a blocking Go library call away from the caller's thread.
    private func offThread<T: Sendable>(_ body: @escaping @Sendable () throws -> T) async throws -> T {
        try await Task.detached { try body() }.value
    }

    private func refuseWritesIfLocal() throws {
        if isClientDecrypt { throw RemoteServerError.readOnly }
    }

    // MARK: - Reading

    public func list(vaultID: String, path: String) async throws -> [RemoteEntry] {
        if let bridge = try localVault(vaultID) {
            return try await offThread {
                try bridge.listDirectory(path: path).map { RemoteEntry(name: $0.name, isDir: $0.isDirectory, size: $0.size) }
            }
        }
        struct Response: Decodable { let entries: [RemoteEntry]? }
        let (data, _) = try await vaultRequest(vaultID) {
            self.request("api/vaults/\(vaultID)/list", query: ["path": path])
        }
        return try decode(Response.self, data).entries ?? []
    }

    public func fileSize(vaultID: String, path: String) async throws -> Int64 {
        if let bridge = try localVault(vaultID) {
            return try await offThread { try bridge.fileSize(path: path) }
        }
        let (_, response) = try await vaultRequest(vaultID) {
            self.request("api/vaults/\(vaultID)/file", method: "HEAD", query: ["path": path])
        }
        guard response.expectedContentLength >= 0 else { throw RemoteServerError.badResponse }
        return response.expectedContentLength
    }

    public func fileData(vaultID: String, path: String) async throws -> Data {
        if let bridge = try localVault(vaultID) {
            return try await offThread { try bridge.decryptFile(path: path) }
        }
        return try await vaultRequest(vaultID) {
            self.request("api/vaults/\(vaultID)/file", query: ["path": path])
        }.0
    }

    /// Reads up to `length` bytes at `offset`; returns fewer at EOF and none past it.
    public func readRange(vaultID: String, path: String, offset: Int64, length: Int) async throws -> Data {
        guard length > 0 else { return Data() }
        if let bridge = try localVault(vaultID) {
            return try await offThread { try bridge.readRange(path: path, offset: offset, length: Int32(length)) }
        }
        let (data, response) = try await vaultRequest(vaultID, accept: [200, 206, 416]) {
            var req = self.request("api/vaults/\(vaultID)/file", query: ["path": path])
            req.setValue("bytes=\(offset)-\(offset + Int64(length) - 1)", forHTTPHeaderField: "Range")
            return req
        }
        switch response.statusCode {
        case 206: return data
        case 416: return Data()
        default:
            // The server ignored the Range header and sent the whole file.
            guard offset < data.count else { return Data() }
            return data.subdata(in: Int(offset)..<min(data.count, Int(offset) + length))
        }
    }

    /// A server-rendered JPEG thumbnail, or nil when the server has no decoder
    /// for this file (HEIC, RAW, or video without ffmpeg).
    public func thumbnail(vaultID: String, path: String, size: Int) async throws -> Data? {
        // A client-decrypt server has no plaintext to thumbnail; the caller
        // renders one from the decrypted file.
        if try localVault(vaultID) != nil { return nil }
        let (data, response) = try await vaultRequest(vaultID, accept: [200, 204]) {
            self.request("api/vaults/\(vaultID)/thumb", query: ["path": path, "size": String(size)])
        }
        return response.statusCode == 204 || data.isEmpty ? nil : data
    }

    // MARK: - Writing

    public func mkdir(vaultID: String, path: String) async throws {
        try refuseWritesIfLocal()
        _ = try await vaultRequest(vaultID) {
            try self.jsonRequest("api/vaults/\(vaultID)/mkdir", body: ["path": path])
        }
    }

    /// Renames or relocates an entry; `to` is the full destination path.
    public func move(vaultID: String, from: String, to: String) async throws {
        try refuseWritesIfLocal()
        _ = try await vaultRequest(vaultID) {
            try self.jsonRequest("api/vaults/\(vaultID)/move", body: ["from": from, "to": to])
        }
    }

    /// Deletes a file, or a directory with everything in it.
    public func delete(vaultID: String, path: String) async throws {
        try refuseWritesIfLocal()
        _ = try await vaultRequest(vaultID) {
            self.request("api/vaults/\(vaultID)/entry", method: "DELETE", query: ["path": path])
        }
    }

    /// Uploads an in-memory file. The server refuses to overwrite.
    public func upload(vaultID: String, directory: String, name: String, data: Data) async throws {
        try refuseWritesIfLocal()
        _ = try await vaultRequest(vaultID) {
            var req = self.request("api/vaults/\(vaultID)/upload", method: "POST",
                                   query: ["path": directory, "name": name])
            req.setValue("application/octet-stream", forHTTPHeaderField: "Content-Type")
            req.httpBody = data
            return req
        }
    }

    /// Streams chunks to the server as the request body, so a large video is
    /// never held whole in memory (and never spooled to a temp file).
    public func upload<S: AsyncSequence>(
        vaultID: String, directory: String, name: String, chunks: S,
        progress: @escaping @Sendable (Int64) -> Void
    ) async throws where S.Element == Data {
        try refuseWritesIfLocal()
        // A streamed body cannot be replayed after a 401, so make sure the
        // server session is live first; this re-unlocks it if it expired.
        _ = try await list(vaultID: vaultID, path: directory)

        var input: InputStream?
        var output: OutputStream?
        Stream.getBoundStreams(withBufferSize: 1 << 20, inputStream: &input, outputStream: &output)
        guard let input, let output else { throw RemoteServerError.badResponse }

        var req = request("api/vaults/\(vaultID)/upload", method: "POST",
                          query: ["path": directory, "name": name])
        req.setValue("application/octet-stream", forHTTPHeaderField: "Content-Type")
        req.httpBodyStream = input
        // Long uploads over a slow link must not trip the per-request timeout.
        req.timeoutInterval = 3600

        output.open()
        let writer = Task {
            defer { output.close() }
            var sent: Int64 = 0
            for try await chunk in chunks {
                try await Self.write(chunk, to: output)
                sent += Int64(chunk.count)
                progress(sent)
            }
        }

        let result: (Data, URLResponse)
        do {
            result = try await session.data(for: req)
        } catch {
            writer.cancel()
            // A failed source read surfaces here as a truncated body; report
            // the read error rather than the network one.
            if case .failure(let readError) = await writer.result { throw readError }
            throw error
        }
        writer.cancel()
        if case .failure(let readError) = await writer.result, !(readError is CancellationError) {
            throw readError
        }
        try check(try httpResponse(result.1), result.0)
    }

    private static func write(_ chunk: Data, to output: OutputStream) async throws {
        var offset = 0
        while offset < chunk.count {
            try Task.checkCancellation()
            guard output.hasSpaceAvailable else {
                try await Task.sleep(for: .milliseconds(2))
                continue
            }
            let n = chunk.withUnsafeBytes { buf in
                output.write(buf.baseAddress!.assumingMemoryBound(to: UInt8.self).advanced(by: offset),
                             maxLength: chunk.count - offset)
            }
            if n < 0 { throw output.streamError ?? RemoteServerError.badResponse }
            offset += n
        }
    }

    // MARK: - Plumbing

    /// Sends a request against an unlocked vault, re-unlocking once if the
    /// server has expired the session.
    private func vaultRequest(
        _ vaultID: String, accept: Set<Int> = [200],
        _ build: () throws -> URLRequest
    ) async throws -> (Data, HTTPURLResponse) {
        var (data, response) = try await raw(build())
        if response.statusCode == 401, let password = password(for: vaultID) {
            try await unlock(vaultID: vaultID, password: password)
            (data, response) = try await raw(build())
        }
        guard accept.contains(response.statusCode) else {
            try check(response, data)
            throw RemoteServerError.http(status: response.statusCode, message: "unexpected status")
        }
        return (data, response)
    }

    private func password(for vaultID: String) -> String? {
        lock.withLock { passwords[vaultID] }
    }

    private func send(_ req: URLRequest) async throws -> (Data, HTTPURLResponse) {
        let (data, response) = try await raw(req)
        try check(response, data)
        return (data, response)
    }

    private func raw(_ req: URLRequest) async throws -> (Data, HTTPURLResponse) {
        let (data, response) = try await session.data(for: req)
        return (data, try httpResponse(response))
    }

    private func httpResponse(_ response: URLResponse) throws -> HTTPURLResponse {
        guard let http = response as? HTTPURLResponse else { throw RemoteServerError.badResponse }
        return http
    }

    private func check(_ response: HTTPURLResponse, _ data: Data) throws {
        guard (200..<300).contains(response.statusCode) else {
            struct APIError: Decodable { let error: String }
            let message = (try? JSONDecoder().decode(APIError.self, from: data))?.error
                ?? HTTPURLResponse.localizedString(forStatusCode: response.statusCode)
            throw RemoteServerError.http(status: response.statusCode, message: message)
        }
    }

    private func decode<T: Decodable>(_ type: T.Type, _ data: Data) throws -> T {
        do {
            return try JSONDecoder().decode(type, from: data)
        } catch {
            throw RemoteServerError.badResponse
        }
    }

    private func jsonRequest(_ path: String, body: [String: String]) throws -> URLRequest {
        var req = request(path, method: "POST")
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try JSONEncoder().encode(body)
        return req
    }

    private func request(_ path: String, method: String = "GET", query: [String: String] = [:]) -> URLRequest {
        var components = URLComponents(url: baseURL, resolvingAgainstBaseURL: false)!
        let basePath = components.path.hasSuffix("/") ? String(components.path.dropLast()) : components.path
        components.percentEncodedPath = basePath + "/" + path
        if !query.isEmpty {
            // Encode strictly: URLComponents leaves "+" alone, which Go's query
            // parser reads as a space, so "a+b.jpg" would name the wrong file.
            components.percentEncodedQuery = query.sorted { $0.key < $1.key }
                .map { "\($0.key)=\(Self.encode($0.value))" }
                .joined(separator: "&")
        }
        var req = URLRequest(url: components.url!)
        req.httpMethod = method
        return req
    }

    private static let unreserved = CharacterSet(charactersIn:
        "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~")

    private static func encode(_ value: String) -> String {
        value.addingPercentEncoding(withAllowedCharacters: unreserved) ?? value
    }
}

/// Adapts one remote vault to the synchronous reads EncryptedResourceLoader
/// makes from its background queue.
public final class RemoteRangeReader: EncryptedRangeReading, @unchecked Sendable {
    let client: RemoteServerClient
    let vaultID: String

    public init(client: RemoteServerClient, vaultID: String) {
        self.client = client
        self.vaultID = vaultID
    }

    public func readRange(path: String, offset: Int64, length: Int32) throws -> Data {
        let client = self.client, vaultID = self.vaultID
        return try blocking { try await client.readRange(vaultID: vaultID, path: path, offset: offset, length: Int(length)) }
    }

    public func fileSize(path: String) throws -> Int64 {
        let client = self.client, vaultID = self.vaultID
        return try blocking { try await client.fileSize(vaultID: vaultID, path: path) }
    }

    private func blocking<T>(_ op: @escaping @Sendable () async throws -> T) throws -> T {
        let sema = DispatchSemaphore(value: 0)
        nonisolated(unsafe) var result: Result<T, Error> = .failure(RemoteServerError.badResponse)
        Task.detached {
            do { result = .success(try await op()) } catch { result = .failure(error) }
            sema.signal()
        }
        sema.wait()
        return try result.get()
    }
}

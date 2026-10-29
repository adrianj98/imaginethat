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
    case http(status: Int, message: String)

    public var errorDescription: String? {
        switch self {
        case .badResponse: return "The server sent an unexpected response"
        case .invalidServerURL: return "Enter a server address like https://nas.local:8347"
        case .wrongPassword: return "Wrong password"
        case .http(_, let message): return "Server: \(message)"
        }
    }
}

/// Talks to an imagethat-web server (webserver/ in this repo).
///
/// The server decrypts; this client only ever sees plaintext in memory. The
/// URLSession is ephemeral with no URL cache, so neither responses nor the
/// session cookie that holds the unlocked vaults reach the disk.
public final class RemoteServerClient: @unchecked Sendable {
    public let baseURL: URL
    private let session: URLSession

    private let lock = NSLock()
    // vault ID → password, in memory only. The server locks idle sessions
    // (default 30 min); with this the client re-unlocks instead of failing.
    private var passwords: [String: String] = [:]

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

    // MARK: - Vaults

    public func listVaults() async throws -> [RemoteVault] {
        struct Response: Decodable { let vaults: [RemoteVault]? }
        let (data, _) = try await send(request("api/vaults"))
        return try decode(Response.self, data).vaults ?? []
    }

    public func unlock(vaultID: String, password: String) async throws {
        var req = request("api/vaults/\(vaultID)/unlock", method: "POST")
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try JSONEncoder().encode(["password": password])
        let (data, response) = try await session.data(for: req)
        let http = try httpResponse(response)
        if http.statusCode == 401 { throw RemoteServerError.wrongPassword }
        try check(http, data)
        lock.withLock { passwords[vaultID] = password }
    }

    public func lock(vaultID: String) async throws {
        lock.withLock { passwords[vaultID] = nil }
        _ = try await send(request("api/vaults/\(vaultID)/lock", method: "POST"))
    }

    /// Ends this client's server session, locking every vault it opened.
    public func logout() async throws {
        lock.withLock { passwords.removeAll() }
        _ = try await send(request("api/logout", method: "POST"))
    }

    // MARK: - Reading

    public func list(vaultID: String, path: String) async throws -> [RemoteEntry] {
        struct Response: Decodable { let entries: [RemoteEntry]? }
        let (data, _) = try await vaultRequest(vaultID) {
            self.request("api/vaults/\(vaultID)/list", query: ["path": path])
        }
        return try decode(Response.self, data).entries ?? []
    }

    public func fileSize(vaultID: String, path: String) async throws -> Int64 {
        let (_, response) = try await vaultRequest(vaultID) {
            self.request("api/vaults/\(vaultID)/file", method: "HEAD", query: ["path": path])
        }
        guard response.expectedContentLength >= 0 else { throw RemoteServerError.badResponse }
        return response.expectedContentLength
    }

    public func fileData(vaultID: String, path: String) async throws -> Data {
        try await vaultRequest(vaultID) {
            self.request("api/vaults/\(vaultID)/file", query: ["path": path])
        }.0
    }

    /// Reads up to `length` bytes at `offset`; returns fewer at EOF and none past it.
    public func readRange(vaultID: String, path: String, offset: Int64, length: Int) async throws -> Data {
        guard length > 0 else { return Data() }
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
        let (data, response) = try await vaultRequest(vaultID, accept: [200, 204]) {
            self.request("api/vaults/\(vaultID)/thumb", query: ["path": path, "size": String(size)])
        }
        return response.statusCode == 204 || data.isEmpty ? nil : data
    }

    // MARK: - Writing

    public func mkdir(vaultID: String, path: String) async throws {
        _ = try await vaultRequest(vaultID) {
            try self.jsonRequest("api/vaults/\(vaultID)/mkdir", body: ["path": path])
        }
    }

    /// Renames or relocates an entry; `to` is the full destination path.
    public func move(vaultID: String, from: String, to: String) async throws {
        _ = try await vaultRequest(vaultID) {
            try self.jsonRequest("api/vaults/\(vaultID)/move", body: ["from": from, "to": to])
        }
    }

    /// Deletes a file, or a directory with everything in it.
    public func delete(vaultID: String, path: String) async throws {
        _ = try await vaultRequest(vaultID) {
            self.request("api/vaults/\(vaultID)/entry", method: "DELETE", query: ["path": path])
        }
    }

    /// Uploads an in-memory file. The server refuses to overwrite.
    public func upload(vaultID: String, directory: String, name: String, data: Data) async throws {
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

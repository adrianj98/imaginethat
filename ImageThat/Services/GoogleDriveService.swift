import Foundation
import Security
import AppKit

public enum GoogleDriveError: LocalizedError {
    case notConnected
    case authFailed(String)
    case authCancelled
    case httpError(Int, String)
    case invalidResponse
    case uploadFailed(String)

    public var errorDescription: String? {
        switch self {
        case .notConnected: return "Not connected to Google Drive"
        case .authFailed(let msg): return "Google Drive authentication failed: \(msg)"
        case .authCancelled: return "Connection cancelled"
        case .httpError(let code, let msg): return "HTTP \(code): \(msg)"
        case .invalidResponse: return "Invalid response from Google Drive"
        case .uploadFailed(let msg): return "Upload failed: \(msg)"
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
}

public struct DriveFolder: Sendable {
    public let id: String
    public let name: String
}

public struct DriveAllEntry: Sendable {
    public let id: String
    public let name: String
    public let isFolder: Bool
    public let size: Int64
    public let modifiedTime: String
}

// MARK: - Auth manager (opens the system browser + local server catches the redirect)

@MainActor
final class GoogleDriveAuthManager {
    static let shared = GoogleDriveAuthManager()
    private var server: LocalOAuthServer?

    func startAuth(clientId: String) async throws -> (code: String, redirectURI: String) {
        let server = LocalOAuthServer()
        self.server = server

        let port = try await server.start()
        let redirectURI = "http://127.0.0.1:\(port)/oauth"

        var components = URLComponents(string: "https://accounts.google.com/o/oauth2/v2/auth")!
        components.queryItems = [
            URLQueryItem(name: "client_id", value: clientId),
            URLQueryItem(name: "redirect_uri", value: redirectURI),
            URLQueryItem(name: "response_type", value: "code"),
            URLQueryItem(name: "scope", value: "https://www.googleapis.com/auth/drive"),
            URLQueryItem(name: "access_type", value: "offline"),
            URLQueryItem(name: "prompt", value: "consent"),
        ]
        guard let authURL = components.url else {
            throw GoogleDriveError.authFailed("Invalid auth URL")
        }

        NSWorkspace.shared.open(authURL)

        let callbackURL = try await server.waitForCallback(timeout: 300)
        self.server = nil

        guard let code = URLComponents(url: callbackURL, resolvingAgainstBaseURL: false)?
            .queryItems?.first(where: { $0.name == "code" })?.value else {
            throw GoogleDriveError.authFailed("No authorization code in redirect")
        }

        return (code: code, redirectURI: redirectURI)
    }

    /// Cancel an in-flight `startAuth` call. The pending continuation throws
    /// `GoogleDriveError.authCancelled`.
    func cancelAuth() {
        server?.cancel()
        server = nil
    }
}

// MARK: - Google Drive Service

public actor GoogleDriveService {
    public static let shared = GoogleDriveService()

    private var clientId: String?
    private var clientSecret: String?
    private var accessToken: String?
    private var tokenExpiry: Date = .distantPast
    private var refreshToken: String?

    private static let udClientId     = "gdrive.clientId"
    private static let udClientSecret = "gdrive.clientSecret"
    private static let udRefreshToken = "gdrive.refreshToken"

    private init() {}

    // MARK: - Connection state

    public var isConnected: Bool { refreshToken != nil }

    public func loadSavedCredentials() {
        let s = AppSettings.shared
        clientId     = s.string(forKey: Self.udClientId)
        clientSecret = s.string(forKey: Self.udClientSecret)
        refreshToken = s.string(forKey: Self.udRefreshToken)
    }

    public func disconnect() {
        accessToken = nil
        refreshToken = nil
        clientId = nil
        clientSecret = nil
        tokenExpiry = .distantPast
        let s = AppSettings.shared
        s.removeObject(forKey: Self.udClientId)
        s.removeObject(forKey: Self.udClientSecret)
        s.removeObject(forKey: Self.udRefreshToken)
    }

    // MARK: - OAuth

    public func authorize(clientId: String, clientSecret: String) async throws {
        self.clientId = clientId
        self.clientSecret = clientSecret
        let s = AppSettings.shared
        s.set(clientId, forKey: Self.udClientId)
        s.set(clientSecret, forKey: Self.udClientSecret)

        // Opens the system browser; local server catches the redirect on http://127.0.0.1:PORT
        let result = try await GoogleDriveAuthManager.shared.startAuth(clientId: clientId)
        try await exchangeCode(result.code, clientId: clientId, clientSecret: clientSecret, redirectURI: result.redirectURI)
    }

    private func exchangeCode(_ code: String, clientId: String, clientSecret: String, redirectURI: String) async throws {
        var request = URLRequest(url: URL(string: "https://oauth2.googleapis.com/token")!)
        request.httpMethod = "POST"
        request.setValue("application/x-www-form-urlencoded", forHTTPHeaderField: "Content-Type")
        request.httpBody = formEncode([
            "code": code,
            "client_id": clientId,
            "client_secret": clientSecret,
            "redirect_uri": redirectURI,
            "grant_type": "authorization_code",
        ])

        let (data, _) = try await URLSession.shared.data(for: request)
        let json = try JSONSerialization.jsonObject(with: data) as? [String: Any] ?? [:]

        guard let newAccessToken = json["access_token"] as? String,
              let newRefreshToken = json["refresh_token"] as? String else {
            let errMsg = json["error_description"] as? String ?? String(data: data, encoding: .utf8) ?? "Unknown"
            throw GoogleDriveError.authFailed(errMsg)
        }

        let expiresIn = json["expires_in"] as? Int ?? 3600
        accessToken = newAccessToken
        tokenExpiry = Date().addingTimeInterval(TimeInterval(expiresIn - 60))
        refreshToken = newRefreshToken
        AppSettings.shared.set(newRefreshToken, forKey: Self.udRefreshToken)
    }

    // MARK: - Token management

    private func validAccessToken() async throws -> String {
        if let token = accessToken, Date() < tokenExpiry { return token }
        try await refreshAccessToken()
        guard let token = accessToken else { throw GoogleDriveError.notConnected }
        return token
    }

    private func refreshAccessToken() async throws {
        guard let rt = refreshToken, let cid = clientId, let cs = clientSecret else {
            throw GoogleDriveError.notConnected
        }

        var request = URLRequest(url: URL(string: "https://oauth2.googleapis.com/token")!)
        request.httpMethod = "POST"
        request.setValue("application/x-www-form-urlencoded", forHTTPHeaderField: "Content-Type")
        request.httpBody = formEncode([
            "refresh_token": rt,
            "client_id": cid,
            "client_secret": cs,
            "grant_type": "refresh_token",
        ])

        let (data, _) = try await URLSession.shared.data(for: request)
        let json = try JSONSerialization.jsonObject(with: data) as? [String: Any] ?? [:]

        guard let newToken = json["access_token"] as? String else {
            throw GoogleDriveError.authFailed("Token refresh failed")
        }

        let expiresIn = json["expires_in"] as? Int ?? 3600
        accessToken = newToken
        tokenExpiry = Date().addingTimeInterval(TimeInterval(expiresIn - 60))
    }

    // MARK: - Drive API: reads

    public struct VaultRootMatch: Sendable {
        public let rootFolderId: String
        /// Folder names from the vault root down to (and including) the originally
        /// requested descendant. Empty if the descendant *is* the vault root.
        public let descendantPath: [String]
    }

    /// Walks up the parent chain from `folderId`, returning the first ancestor
    /// that contains `gocryptfs.conf` along with the chain of plaintext folder
    /// names from that ancestor down to `folderId`. Returns nil if no ancestor
    /// is a vault root (stops at My Drive).
    public func findVaultRoot(forDescendant folderId: String, maxDepth: Int = 8) async -> VaultRootMatch? {
        var current = folderId
        var pathComponents: [String] = []
        for _ in 0..<maxDepth {
            guard let info = try? await folderInfo(of: current),
                  let parent = info.parents.first else { return nil }
            pathComponents.insert(info.name, at: 0)
            if let hasConf = try? await fileExists(name: "gocryptfs.conf", in: parent), hasConf {
                return VaultRootMatch(rootFolderId: parent, descendantPath: pathComponents)
            }
            current = parent
        }
        return nil
    }

    private struct FolderInfo: Sendable {
        let name: String
        let parents: [String]
    }

    private func folderInfo(of folderId: String) async throws -> FolderInfo {
        let token = try await validAccessToken()
        var components = URLComponents(string: "https://www.googleapis.com/drive/v3/files/\(folderId)")!
        components.queryItems = [URLQueryItem(name: "fields", value: "name,parents")]
        var request = URLRequest(url: components.url!)
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        let (data, response) = try await URLSession.shared.data(for: request)
        try checkHTTP(response, data: data)
        let json = try JSONSerialization.jsonObject(with: data) as? [String: Any] ?? [:]
        return FolderInfo(
            name: json["name"] as? String ?? "",
            parents: json["parents"] as? [String] ?? []
        )
    }

    public func fileExists(name: String, in folderId: String) async throws -> Bool {
        let token = try await validAccessToken()
        var components = URLComponents(string: "https://www.googleapis.com/drive/v3/files")!
        let escapedName = name.replacingOccurrences(of: "'", with: "\\'")
        components.queryItems = [
            URLQueryItem(name: "q", value: "name='\(escapedName)' and '\(folderId)' in parents and trashed=false"),
            URLQueryItem(name: "fields", value: "files(id)"),
            URLQueryItem(name: "pageSize", value: "1"),
        ]
        var request = URLRequest(url: components.url!)
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        let (data, response) = try await URLSession.shared.data(for: request)
        try checkHTTP(response, data: data)
        let json = try JSONSerialization.jsonObject(with: data) as? [String: Any] ?? [:]
        let files = json["files"] as? [[String: Any]] ?? []
        return !files.isEmpty
    }

    public func listAllEntries(in folderId: String) async throws -> [DriveAllEntry] {
        let token = try await validAccessToken()
        var components = URLComponents(string: "https://www.googleapis.com/drive/v3/files")!
        components.queryItems = [
            URLQueryItem(name: "q", value: "'\(folderId)' in parents and trashed=false"),
            URLQueryItem(name: "fields", value: "files(id,name,mimeType,size,modifiedTime)"),
            URLQueryItem(name: "pageSize", value: "1000"),
        ]
        var request = URLRequest(url: components.url!)
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        let (data, response) = try await URLSession.shared.data(for: request)
        try checkHTTP(response, data: data)
        let json = try JSONSerialization.jsonObject(with: data) as? [String: Any] ?? [:]
        return (json["files"] as? [[String: Any]] ?? []).compactMap { dict in
            guard let id = dict["id"] as? String, let name = dict["name"] as? String else { return nil }
            let mimeType = dict["mimeType"] as? String ?? ""
            let isFolder = mimeType == "application/vnd.google-apps.folder"
            let size = Int64(dict["size"] as? String ?? "0") ?? 0
            let modifiedTime = dict["modifiedTime"] as? String ?? ""
            return DriveAllEntry(id: id, name: name, isFolder: isFolder, size: size, modifiedTime: modifiedTime)
        }
    }

    public func listFiles(in folderId: String) async throws -> [DriveFile] {
        let token = try await validAccessToken()
        let mimeFilter = "mimeType contains 'image/' or mimeType contains 'video/'"
        var components = URLComponents(string: "https://www.googleapis.com/drive/v3/files")!
        components.queryItems = [
            URLQueryItem(name: "q", value: "'\(folderId)' in parents and (\(mimeFilter)) and trashed=false"),
            URLQueryItem(name: "fields", value: "files(id,name,mimeType,size,thumbnailLink,modifiedTime)"),
            URLQueryItem(name: "pageSize", value: "1000"),
            URLQueryItem(name: "orderBy", value: "name"),
        ]
        var request = URLRequest(url: components.url!)
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")

        let (data, response) = try await URLSession.shared.data(for: request)
        try checkHTTP(response, data: data)

        let json = try JSONSerialization.jsonObject(with: data) as? [String: Any] ?? [:]
        return (json["files"] as? [[String: Any]] ?? []).compactMap(parseFile)
    }

    public func listFolders(in folderId: String) async throws -> [DriveFolder] {
        let token = try await validAccessToken()
        var components = URLComponents(string: "https://www.googleapis.com/drive/v3/files")!
        components.queryItems = [
            URLQueryItem(name: "q", value: "'\(folderId)' in parents and mimeType='application/vnd.google-apps.folder' and trashed=false"),
            URLQueryItem(name: "fields", value: "files(id,name)"),
            URLQueryItem(name: "pageSize", value: "1000"),
            URLQueryItem(name: "orderBy", value: "name"),
        ]
        var request = URLRequest(url: components.url!)
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")

        let (data, response) = try await URLSession.shared.data(for: request)
        try checkHTTP(response, data: data)

        let json = try JSONSerialization.jsonObject(with: data) as? [String: Any] ?? [:]
        return (json["files"] as? [[String: Any]] ?? []).compactMap { dict in
            guard let id = dict["id"] as? String, let name = dict["name"] as? String else { return nil }
            return DriveFolder(id: id, name: name)
        }
    }

    public func downloadData(fileId: String) async throws -> Data {
        let token = try await validAccessToken()
        let url = URL(string: "https://www.googleapis.com/drive/v3/files/\(fileId)?alt=media")!
        var request = URLRequest(url: url)
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        let (data, response) = try await URLSession.shared.data(for: request)
        try checkHTTP(response, data: data)
        return data
    }

    public func downloadRange(fileId: String, offset: Int64, length: Int) async throws -> Data {
        let token = try await validAccessToken()
        let url = URL(string: "https://www.googleapis.com/drive/v3/files/\(fileId)?alt=media")!
        var request = URLRequest(url: url)
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        request.setValue("bytes=\(offset)-\(offset + Int64(length) - 1)", forHTTPHeaderField: "Range")
        let (data, response) = try await URLSession.shared.data(for: request)
        if let http = response as? HTTPURLResponse, http.statusCode == 416 { return Data() }
        try checkHTTP(response, data: data)
        return data
    }

    public func downloadThumbnail(urlString: String) async throws -> Data {
        let token = try await validAccessToken()
        guard let url = URL(string: urlString) else { throw GoogleDriveError.invalidResponse }
        var request = URLRequest(url: url)
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        let (data, response) = try await URLSession.shared.data(for: request)
        try checkHTTP(response, data: data)
        return data
    }

    public func fileSize(fileId: String) async throws -> Int64 {
        let token = try await validAccessToken()
        var components = URLComponents(string: "https://www.googleapis.com/drive/v3/files/\(fileId)")!
        components.queryItems = [URLQueryItem(name: "fields", value: "size")]
        var request = URLRequest(url: components.url!)
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        let (data, response) = try await URLSession.shared.data(for: request)
        try checkHTTP(response, data: data)
        let json = try JSONSerialization.jsonObject(with: data) as? [String: Any] ?? [:]
        return Int64(json["size"] as? String ?? "0") ?? 0
    }

    // MARK: - Drive API: writes

    public func uploadFile(name: String, parentId: String, data: Data, mimeType: String) async throws -> DriveFile {
        let token = try await validAccessToken()
        if data.count > 5 * 1024 * 1024 {
            return try await resumableUpload(name: name, parentId: parentId, data: data, mimeType: mimeType, token: token)
        }
        return try await multipartUpload(name: name, parentId: parentId, data: data, mimeType: mimeType, token: token)
    }

    private func multipartUpload(name: String, parentId: String, data: Data, mimeType: String, token: String) async throws -> DriveFile {
        let boundary = "boundary-\(UUID().uuidString)"
        var components = URLComponents(string: "https://www.googleapis.com/upload/drive/v3/files")!
        components.queryItems = [
            URLQueryItem(name: "uploadType", value: "multipart"),
            URLQueryItem(name: "fields", value: "id,name,mimeType,size,thumbnailLink,modifiedTime"),
        ]
        var request = URLRequest(url: components.url!)
        request.httpMethod = "POST"
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        request.setValue("multipart/related; boundary=\(boundary)", forHTTPHeaderField: "Content-Type")

        let metadata = try JSONSerialization.data(withJSONObject: ["name": name, "parents": [parentId]])
        var body = Data()
        body.append("--\(boundary)\r\nContent-Type: application/json; charset=UTF-8\r\n\r\n".data(using: .utf8)!)
        body.append(metadata)
        body.append("\r\n--\(boundary)\r\nContent-Type: \(mimeType)\r\n\r\n".data(using: .utf8)!)
        body.append(data)
        body.append("\r\n--\(boundary)--".data(using: .utf8)!)
        request.httpBody = body

        let (responseData, response) = try await URLSession.shared.data(for: request)
        try checkHTTP(response, data: responseData)
        return try parseFileResponse(responseData)
    }

    private func resumableUpload(name: String, parentId: String, data: Data, mimeType: String, token: String) async throws -> DriveFile {
        var initComponents = URLComponents(string: "https://www.googleapis.com/upload/drive/v3/files")!
        initComponents.queryItems = [
            URLQueryItem(name: "uploadType", value: "resumable"),
            URLQueryItem(name: "fields", value: "id,name,mimeType,size,thumbnailLink,modifiedTime"),
        ]
        var initRequest = URLRequest(url: initComponents.url!)
        initRequest.httpMethod = "POST"
        initRequest.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        initRequest.setValue("application/json; charset=UTF-8", forHTTPHeaderField: "Content-Type")
        initRequest.setValue(mimeType, forHTTPHeaderField: "X-Upload-Content-Type")
        initRequest.setValue("\(data.count)", forHTTPHeaderField: "X-Upload-Content-Length")
        initRequest.httpBody = try JSONSerialization.data(withJSONObject: ["name": name, "parents": [parentId]])

        let (_, initResponse) = try await URLSession.shared.data(for: initRequest)
        guard let http = initResponse as? HTTPURLResponse,
              let locationStr = http.value(forHTTPHeaderField: "Location"),
              let uploadURL = URL(string: locationStr) else {
            throw GoogleDriveError.uploadFailed("No upload location in response")
        }

        var uploadRequest = URLRequest(url: uploadURL)
        uploadRequest.httpMethod = "PUT"
        uploadRequest.setValue(mimeType, forHTTPHeaderField: "Content-Type")
        uploadRequest.httpBody = data

        let (responseData, response) = try await URLSession.shared.data(for: uploadRequest)
        try checkHTTP(response, data: responseData)
        return try parseFileResponse(responseData)
    }

    public func updateFile(fileId: String, data: Data, mimeType: String) async throws {
        let token = try await validAccessToken()
        var components = URLComponents(string: "https://www.googleapis.com/upload/drive/v3/files/\(fileId)")!
        components.queryItems = [URLQueryItem(name: "uploadType", value: "media")]
        var request = URLRequest(url: components.url!)
        request.httpMethod = "PATCH"
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        request.setValue(mimeType, forHTTPHeaderField: "Content-Type")
        request.httpBody = data
        let (responseData, response) = try await URLSession.shared.data(for: request)
        try checkHTTP(response, data: responseData)
    }

    public func deleteFile(fileId: String) async throws {
        let token = try await validAccessToken()
        let url = URL(string: "https://www.googleapis.com/drive/v3/files/\(fileId)")!
        var request = URLRequest(url: url)
        request.httpMethod = "DELETE"
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        let (_, response) = try await URLSession.shared.data(for: request)
        if let http = response as? HTTPURLResponse, http.statusCode != 204, http.statusCode != 200 {
            throw GoogleDriveError.httpError(http.statusCode, "Delete failed")
        }
    }

    public func moveFile(fileId: String, from oldParentId: String, to newParentId: String) async throws {
        let token = try await validAccessToken()
        var components = URLComponents(string: "https://www.googleapis.com/drive/v3/files/\(fileId)")!
        components.queryItems = [
            URLQueryItem(name: "addParents", value: newParentId),
            URLQueryItem(name: "removeParents", value: oldParentId),
            URLQueryItem(name: "fields", value: "id"),
        ]
        var request = URLRequest(url: components.url!)
        request.httpMethod = "PATCH"
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = "{}".data(using: .utf8)
        let (responseData, response) = try await URLSession.shared.data(for: request)
        try checkHTTP(response, data: responseData)
    }

    public func createFolder(name: String, parentId: String) async throws -> DriveFolder {
        let token = try await validAccessToken()
        var request = URLRequest(url: URL(string: "https://www.googleapis.com/drive/v3/files")!)
        request.httpMethod = "POST"
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try JSONSerialization.data(withJSONObject: [
            "name": name,
            "mimeType": "application/vnd.google-apps.folder",
            "parents": [parentId]
        ])
        let (responseData, response) = try await URLSession.shared.data(for: request)
        try checkHTTP(response, data: responseData)
        let json = try JSONSerialization.jsonObject(with: responseData) as? [String: Any] ?? [:]
        guard let id = json["id"] as? String, let folderName = json["name"] as? String else {
            throw GoogleDriveError.invalidResponse
        }
        return DriveFolder(id: id, name: folderName)
    }

    // MARK: - Helpers

    private func checkHTTP(_ response: URLResponse, data: Data) throws {
        guard let http = response as? HTTPURLResponse else { return }
        guard (200..<300).contains(http.statusCode) else {
            let msg = (try? JSONSerialization.jsonObject(with: data) as? [String: Any])
                .flatMap { $0["error"] as? [String: Any] }
                .flatMap { $0["message"] as? String }
                ?? String(data: data, encoding: .utf8)
                ?? "Unknown error"
            throw GoogleDriveError.httpError(http.statusCode, msg)
        }
    }

    private func parseFile(_ dict: [String: Any]) -> DriveFile? {
        guard let id = dict["id"] as? String,
              let name = dict["name"] as? String,
              let mimeType = dict["mimeType"] as? String else { return nil }
        return DriveFile(
            id: id,
            name: name,
            mimeType: mimeType,
            size: Int64(dict["size"] as? String ?? "0") ?? 0,
            thumbnailLink: dict["thumbnailLink"] as? String,
            modifiedTime: dict["modifiedTime"] as? String ?? ""
        )
    }

    private func parseFileResponse(_ data: Data) throws -> DriveFile {
        let json = try JSONSerialization.jsonObject(with: data) as? [String: Any] ?? [:]
        guard let file = parseFile(json) else { throw GoogleDriveError.invalidResponse }
        return file
    }

    private func formEncode(_ params: [String: String]) -> Data {
        params.map { k, v in
            "\(k)=\(v.addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed) ?? v)"
        }.joined(separator: "&").data(using: .utf8) ?? Data()
    }
}

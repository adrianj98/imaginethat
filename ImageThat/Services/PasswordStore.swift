import Foundation
import CryptoKit

/// On-disk storage for vault passwords.
/// When encryption is enabled (default): AES-GCM encrypted with a master key from Keychain.
/// When encryption is disabled (debug mode): plain JSON file.
public final class PasswordStore: @unchecked Sendable {
    public static let shared = PasswordStore()

    private let encryptedFileURL: URL
    private let plaintextFileURL: URL

    /// When true, passwords are stored as plain JSON (no encryption, no Touch ID needed).
    /// Set via Preferences → Security → "Store passwords without encryption".
    public static var encryptionDisabled: Bool {
        get { AppSettings.shared.bool(forKey: "imagethat_passwords_plaintext") }
        set { AppSettings.shared.set(newValue, forKey: "imagethat_passwords_plaintext") }
    }

    private init() {
        let dir = AppPaths.passwordsDir
        encryptedFileURL = dir.appendingPathComponent("vault-passwords.enc")
        plaintextFileURL = dir.appendingPathComponent("vault-passwords.json")
    }

    private var activeFileURL: URL {
        Self.encryptionDisabled ? plaintextFileURL : encryptedFileURL
    }

    // MARK: - Public API

    /// Load all vault passwords. masterKey is ignored when encryption is disabled.
    public func loadAll(masterKey: Data? = nil) throws -> [String: String] {
        if Self.encryptionDisabled {
            return try loadPlaintext()
        }
        guard let masterKey else { return [:] }
        return try loadEncrypted(masterKey: masterKey)
    }

    /// Save a vault password. masterKey is ignored when encryption is disabled.
    public func save(password: String, forVault path: String, masterKey: Data? = nil) throws {
        var passwords = (try? loadAll(masterKey: masterKey)) ?? [:]
        passwords[path] = password
        try writeAll(passwords, masterKey: masterKey)
    }

    /// Remove a vault password.
    public func delete(vault path: String, masterKey: Data? = nil) throws {
        var passwords = (try? loadAll(masterKey: masterKey)) ?? [:]
        passwords.removeValue(forKey: path)
        if passwords.isEmpty {
            deleteAll()
        } else {
            try writeAll(passwords, masterKey: masterKey)
        }
    }

    /// Delete all saved passwords.
    public func deleteAll() {
        try? FileManager.default.removeItem(at: encryptedFileURL)
        try? FileManager.default.removeItem(at: plaintextFileURL)
    }

    /// List vault paths that have saved passwords.
    public func savedVaultPaths(masterKey: Data? = nil) -> [String] {
        guard let passwords = try? loadAll(masterKey: masterKey) else { return [] }
        return Array(passwords.keys)
    }

    /// Check if any passwords are saved.
    public var hasPasswords: Bool {
        FileManager.default.fileExists(atPath: encryptedFileURL.path) ||
        FileManager.default.fileExists(atPath: plaintextFileURL.path)
    }

    // MARK: - Private

    private func loadPlaintext() throws -> [String: String] {
        guard FileManager.default.fileExists(atPath: plaintextFileURL.path) else { return [:] }
        let data = try Data(contentsOf: plaintextFileURL)
        return (try? JSONSerialization.jsonObject(with: data) as? [String: String]) ?? [:]
    }

    private func loadEncrypted(masterKey: Data) throws -> [String: String] {
        guard FileManager.default.fileExists(atPath: encryptedFileURL.path) else { return [:] }
        let encrypted = try Data(contentsOf: encryptedFileURL)
        let json = try decrypt(encrypted, key: masterKey)
        return (try? JSONSerialization.jsonObject(with: json) as? [String: String]) ?? [:]
    }

    private func writeAll(_ passwords: [String: String], masterKey: Data?) throws {
        let json = try JSONSerialization.data(withJSONObject: passwords, options: .sortedKeys)
        if Self.encryptionDisabled {
            try json.write(to: plaintextFileURL, options: .atomic)
        } else {
            guard let masterKey else { throw PasswordStoreError.encryptionFailed }
            let encrypted = try encrypt(json, key: masterKey)
            try encrypted.write(to: encryptedFileURL, options: .atomic)
        }
    }

    private func encrypt(_ plaintext: Data, key: Data) throws -> Data {
        let symmetricKey = SymmetricKey(data: key)
        let sealed = try AES.GCM.seal(plaintext, using: symmetricKey)
        guard let combined = sealed.combined else { throw PasswordStoreError.encryptionFailed }
        return combined
    }

    private func decrypt(_ combined: Data, key: Data) throws -> Data {
        let symmetricKey = SymmetricKey(data: key)
        let box = try AES.GCM.SealedBox(combined: combined)
        return try AES.GCM.open(box, using: symmetricKey)
    }
}

public enum PasswordStoreError: LocalizedError {
    case encryptionFailed
    case decryptionFailed

    public var errorDescription: String? {
        switch self {
        case .encryptionFailed: return "Failed to encrypt passwords"
        case .decryptionFailed: return "Failed to decrypt passwords"
        }
    }
}

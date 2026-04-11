import Foundation
import Security
import LocalAuthentication

public enum KeychainError: LocalizedError {
    case saveFailed(OSStatus)
    case retrievalFailed(OSStatus)
    case deleteFailed(OSStatus)
    case userCancelled

    public var errorDescription: String? {
        switch self {
        case .saveFailed(let status): return "Keychain save failed: \(status)"
        case .retrievalFailed(let status): return "Keychain retrieval failed: \(status)"
        case .deleteFailed(let status): return "Keychain delete failed: \(status)"
        case .userCancelled: return "Authentication cancelled"
        }
    }
}

public final class KeychainManager: @unchecked Sendable {
    public static let shared = KeychainManager()

    private let service = "com.adrianm.imagethat.vault"

    private init() {}

    // MARK: - Public API

    /// Save a vault password to the Keychain.
    public func savePassword(_ password: String, forVault path: String) throws {
        guard let passwordData = password.data(using: .utf8) else { return }

        // Delete any existing entry first
        let deleteQuery: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: path
        ]
        SecItemDelete(deleteQuery as CFDictionary)

        let addQuery: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: path,
            kSecValueData as String: passwordData,
            kSecAttrAccessible as String: kSecAttrAccessibleWhenUnlockedThisDeviceOnly
        ]

        let status = SecItemAdd(addQuery as CFDictionary, nil)
        guard status == errSecSuccess else {
            throw KeychainError.saveFailed(status)
        }
        UserDefaults.standard.set(true, forKey: "vault_saved_\(path)")
    }

    /// Retrieve a vault password. Prompts Touch ID first, then reads from Keychain.
    /// Returns nil if no password is saved. Throws on auth cancellation.
    public func getPassword(forVault path: String) throws -> String? {
        // Authenticate with Touch ID first
        let context = LAContext()
        var error: NSError?

        // Prefer biometrics, fall back to device password
        let policy: LAPolicy = context.canEvaluatePolicy(.deviceOwnerAuthenticationWithBiometrics, error: &error)
            ? .deviceOwnerAuthenticationWithBiometrics
            : .deviceOwnerAuthentication

        guard context.canEvaluatePolicy(policy, error: &error) else { return nil }

        var authResult: Result<Bool, Error> = .success(false)
        let semaphore = DispatchSemaphore(value: 0)

        context.evaluatePolicy(policy, localizedReason: "Unlock encrypted vault") { success, authError in
            if success {
                authResult = .success(true)
            } else if let authError {
                authResult = .failure(authError)
            }
            semaphore.signal()
        }
        semaphore.wait()

        switch authResult {
        case .success(true): break
        case .success(false): return nil
        case .failure(let err):
            let laError = err as NSError
            if laError.code == LAError.userCancel.rawValue ||
               laError.code == LAError.appCancel.rawValue {
                throw KeychainError.userCancelled
            }
            return nil
        }

        // Touch ID passed — now read from Keychain
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: path,
            kSecReturnData as String: true
        ]

        var result: CFTypeRef?
        let status = SecItemCopyMatching(query as CFDictionary, &result)

        switch status {
        case errSecSuccess:
            guard let data = result as? Data,
                  let password = String(data: data, encoding: .utf8) else { return nil }
            return password
        case errSecItemNotFound:
            return nil
        default:
            throw KeychainError.retrievalFailed(status)
        }
    }

    /// Check if a password is saved for a vault (without triggering any prompt).
    /// Uses a UserDefaults flag to avoid Keychain access which can trigger password dialogs.
    public func hasPassword(forVault path: String) -> Bool {
        UserDefaults.standard.bool(forKey: "vault_saved_\(path)")
    }

    /// Delete a saved vault password.
    public func deletePassword(forVault path: String) throws {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: path
        ]

        let status = SecItemDelete(query as CFDictionary)
        guard status == errSecSuccess || status == errSecItemNotFound else {
            throw KeychainError.deleteFailed(status)
        }
        UserDefaults.standard.removeObject(forKey: "vault_saved_\(path)")
    }

    /// List all vault paths that have saved passwords.
    public func savedVaultPaths() -> [String] {
        let defaults = UserDefaults.standard.dictionaryRepresentation()
        return defaults.keys
            .filter { $0.hasPrefix("vault_saved_") }
            .compactMap { key -> String? in
                guard defaults[key] as? Bool == true else { return nil }
                return String(key.dropFirst("vault_saved_".count))
            }
    }

    /// Check if biometrics (Touch ID) are available on this Mac.
    public var isBiometricAvailable: Bool {
        let context = LAContext()
        return context.canEvaluatePolicy(.deviceOwnerAuthenticationWithBiometrics, error: nil)
    }
}

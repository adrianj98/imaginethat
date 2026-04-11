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

/// Manages a single master encryption key in the Keychain, protected by Touch ID.
/// The master key is used to encrypt/decrypt vault passwords stored in a local file.
public final class KeychainManager: @unchecked Sendable {
    public static let shared = KeychainManager()

    private let service = "com.adrianm.imagethat"
    private let account = "master-key"
    private let hasKeyFlag = "imagethat_has_master_key"

    private init() {}

    // MARK: - Master Key

    /// Save a 32-byte master key to the Keychain.
    public func saveMasterKey(_ key: Data) throws {
        // Delete existing first
        let deleteQuery: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account
        ]
        SecItemDelete(deleteQuery as CFDictionary)

        let addQuery: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecValueData as String: key,
            kSecAttrAccessible as String: kSecAttrAccessibleWhenUnlockedThisDeviceOnly
        ]

        let status = SecItemAdd(addQuery as CFDictionary, nil)
        guard status == errSecSuccess else {
            throw KeychainError.saveFailed(status)
        }
        UserDefaults.standard.set(true, forKey: hasKeyFlag)
    }

    /// Retrieve the master key. Authenticates with Touch ID first, then reads from Keychain.
    /// Returns nil if no key is saved. Throws on auth cancellation.
    public func getMasterKey() throws -> Data? {
        // Touch ID first
        let context = LAContext()
        var error: NSError?
        let policy: LAPolicy = context.canEvaluatePolicy(.deviceOwnerAuthenticationWithBiometrics, error: &error)
            ? .deviceOwnerAuthenticationWithBiometrics
            : .deviceOwnerAuthentication

        guard context.canEvaluatePolicy(policy, error: &error) else { return nil }

        var authResult: Result<Bool, Error> = .success(false)
        let semaphore = DispatchSemaphore(value: 0)

        context.evaluatePolicy(policy, localizedReason: "Unlock saved vault passwords") { success, authError in
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

        // Touch ID passed — read from Keychain
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecReturnData as String: true,
            kSecUseAuthenticationContext as String: context,
            kSecUseAuthenticationUI as String: kSecUseAuthenticationUIFail
        ]

        var result: CFTypeRef?
        let status = SecItemCopyMatching(query as CFDictionary, &result)

        switch status {
        case errSecSuccess:
            return result as? Data
        case errSecItemNotFound:
            UserDefaults.standard.removeObject(forKey: hasKeyFlag)
            return nil
        default:
            throw KeychainError.retrievalFailed(status)
        }
    }

    /// Check if a master key exists without triggering any prompt.
    public func hasMasterKey() -> Bool {
        UserDefaults.standard.bool(forKey: hasKeyFlag)
    }

    /// Delete the master key from Keychain.
    public func deleteMasterKey() {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account
        ]
        SecItemDelete(query as CFDictionary)
        UserDefaults.standard.removeObject(forKey: hasKeyFlag)
    }

    /// Check if biometrics (Touch ID / Face ID) are available.
    public var isBiometricAvailable: Bool {
        let context = LAContext()
        return context.canEvaluatePolicy(.deviceOwnerAuthenticationWithBiometrics, error: nil)
    }
}

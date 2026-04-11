import SwiftUI
import LocalAuthentication

public struct VaultUnlockSheet: View {
    @EnvironmentObject var appState: AppState
    @State private var password = ""
    @State private var rememberPassword = true
    @FocusState private var isPasswordFocused: Bool

    private var biometricAvailable: Bool {
        KeychainManager.shared.isBiometricAvailable
    }

    private var biometricLabel: String {
        let context = LAContext()
        _ = context.canEvaluatePolicy(.deviceOwnerAuthenticationWithBiometrics, error: nil)
        switch context.biometryType {
        case .touchID: return "Remember with Touch ID"
        case .faceID: return "Remember with Face ID"
        default: return "Remember password"
        }
    }

    public init() {}

    public var body: some View {
        VStack(spacing: 20) {
            Image(systemName: "lock.shield.fill")
                .font(.system(size: 48))
                .foregroundStyle(.secondary)

            Text("Unlock Encrypted Vault")
                .font(.title2)
                .fontWeight(.semibold)

            if let url = appState.pendingVaultURL {
                Text(url.lastPathComponent)
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
            }

            SecureField("Password", text: $password)
                .textFieldStyle(.roundedBorder)
                .frame(width: 300)
                .focused($isPasswordFocused)
                .onSubmit {
                    unlock()
                }

            Toggle(biometricLabel, isOn: $rememberPassword)
                .toggleStyle(.checkbox)

            HStack(spacing: 12) {
                Button("Cancel") {
                    appState.showVaultUnlock = false
                    appState.pendingVaultURL = nil
                    password = ""
                }
                .keyboardShortcut(.cancelAction)

                Button("Unlock") {
                    unlock()
                }
                .keyboardShortcut(.defaultAction)
                .disabled(password.isEmpty)
            }
        }
        .padding(40)
        .onAppear {
            isPasswordFocused = true
        }
    }

    private func unlock() {
        appState.unlockVault(password: password, saveToKeychain: rememberPassword)
        appState.showVaultUnlock = false
        password = ""
    }
}

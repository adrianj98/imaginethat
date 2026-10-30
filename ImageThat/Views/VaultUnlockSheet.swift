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

    /// A server URL with no vault: the prompt is for a client-decrypt
    /// server's access password rather than a vault password.
    private var isServerLogin: Bool {
        appState.pendingVaultURL.flatMap(RemoteLocation.parse).map { $0.vaultID == nil } ?? false
    }

    public init() {}

    private var vaultName: String {
        appState.pendingVaultDisplayName ?? appState.pendingVaultURL?.lastPathComponent ?? "Vault"
    }

    private var vaultLocation: String? {
        guard let url = appState.pendingVaultURL, url.isFileURL else { return nil }
        return (url.path as NSString).abbreviatingWithTildeInPath + " · gocryptfs"
    }

    public var body: some View {
        ZStack {
            Glass.lightCanvas.ignoresSafeArea()

            VStack(spacing: 10) {
                Image(systemName: "lock.shield")
                    .font(.system(size: 44, weight: .light))
                Text(isServerLogin ? "\(vaultName) needs its access password" : "\(vaultName) is locked")
            }
            .foregroundStyle(.tertiary)

            Color.black.opacity(0.16).ignoresSafeArea()

            card
        }
        .onAppear {
            isPasswordFocused = true
        }
    }

    private var card: some View {
        VStack(spacing: 22) {
            VStack(spacing: 12) {
                Image(systemName: "lock.shield")
                    .font(.system(size: 32, weight: .medium))
                    .foregroundStyle(.white)
                    .frame(width: 64, height: 64)
                    .background(RoundedRectangle(cornerRadius: 18, style: .continuous).fill(Color.accentColor))
                    .shadow(color: Color.accentColor.opacity(0.35), radius: 12, y: 8)
                VStack(spacing: 4) {
                    Text(isServerLogin ? "Log in to \u{201C}\(vaultName)\u{201D}" : "Unlock \u{201C}\(vaultName)\u{201D}")
                        .font(.system(size: 17, weight: .bold))
                        .multilineTextAlignment(.center)
                    Text(isServerLogin ? "Vaults on this server are decrypted on this Mac; their passwords never leave it."
                         : "Decrypted in memory only — nothing is written to disk.")
                        .font(.system(size: 12))
                        .foregroundStyle(.secondary)
                        .multilineTextAlignment(.center)
                }
            }

            VStack(alignment: .leading, spacing: 10) {
                SecureField(isServerLogin ? "Server access password" : "Password", text: $password)
                    .textFieldStyle(.plain)
                    .font(.system(size: 14))
                    .padding(.horizontal, 14)
                    .frame(height: 40)
                    .background(RoundedRectangle(cornerRadius: 12, style: .continuous).fill(Color.black.opacity(0.06)))
                    .overlay(RoundedRectangle(cornerRadius: 12, style: .continuous)
                        .stroke(isPasswordFocused ? Color.accentColor : .clear, lineWidth: 2))
                    .focused($isPasswordFocused)
                    .onSubmit {
                        unlock()
                    }

                Toggle(isOn: $rememberPassword) {
                    Label(biometricLabel, systemImage: biometricAvailable ? "touchid" : "key")
                        .font(.system(size: 13))
                }
                .toggleStyle(.switch)
                .controlSize(.small)
                .padding(.horizontal, 14)
                .frame(height: 40)
                .background(RoundedRectangle(cornerRadius: 12, style: .continuous).fill(Color.black.opacity(0.04)))

                if let vaultLocation {
                    Text(vaultLocation)
                        .font(.system(size: 11))
                        .foregroundStyle(Glass.label)
                        .lineLimit(1)
                        .truncationMode(.middle)
                        .padding(.horizontal, 4)
                }
            }

            HStack(spacing: 10) {
                Button {
                    appState.showVaultUnlock = false
                    appState.pendingVaultURL = nil
                    appState.pendingVaultDisplayName = nil
                    password = ""
                } label: {
                    Text("Cancel").frame(maxWidth: .infinity)
                }
                .buttonStyle(PillButtonStyle(height: 40))
                .keyboardShortcut(.cancelAction)

                Button {
                    unlock()
                } label: {
                    Text("Unlock").frame(maxWidth: .infinity)
                }
                .buttonStyle(PillButtonStyle(kind: .prominent, height: 40))
                .keyboardShortcut(.defaultAction)
                .disabled(password.isEmpty)
                .opacity(password.isEmpty ? 0.5 : 1)
            }
        }
        .padding(.horizontal, 28)
        .padding(.top, 28)
        .padding(.bottom, 24)
        .frame(width: 420)
        .glassCard(radius: 22)
    }

    private func unlock() {
        appState.unlockVault(password: password, saveToKeychain: rememberPassword)
        appState.showVaultUnlock = false
        password = ""
    }
}

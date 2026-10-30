import SwiftUI

/// Asks for the address of an imagethat-web server and checks it answers.
public struct AddServerSheet: View {
    @EnvironmentObject var appState: AppState
    @State private var address = ""
    @State private var accessPassword = ""
    /// Set once the server says it is client-decrypt and wants a login.
    @State private var needsAccessPassword = false
    @State private var error: String?
    @State private var isConnecting = false
    @FocusState private var isAddressFocused: Bool

    public init() {}

    public var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text("Connect to Server")
                .font(.title2)
                .fontWeight(.semibold)

            Text("Browse vaults on a machine running imagethat-web. Depending on how it is set up, either the server decrypts what you view, or it sends only encrypted files and this Mac decrypts them, so vault passwords never leave it.")
                .font(.callout)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)

            TextField("https://nas.local:8347", text: $address)
                .textFieldStyle(.roundedBorder)
                .focused($isAddressFocused)
                .onSubmit { connect() }
                .disabled(needsAccessPassword)

            if needsAccessPassword {
                Text("This server keeps vaults encrypted and decrypts nothing itself. Enter its access password; vault passwords stay on this Mac.")
                    .font(.callout)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                SecureField("Server access password", text: $accessPassword)
                    .textFieldStyle(.roundedBorder)
                    .onSubmit { connect() }
            }

            if let error {
                Text(error)
                    .font(.caption)
                    .foregroundStyle(.red)
            }

            HStack {
                if isConnecting {
                    ProgressView().scaleEffect(0.6)
                }
                Spacer()
                Button("Cancel") {
                    appState.showAddServer = false
                }
                .keyboardShortcut(.cancelAction)

                Button("Connect") { connect() }
                    .keyboardShortcut(.defaultAction)
                    .disabled(address.trimmingCharacters(in: .whitespaces).isEmpty || isConnecting
                              || (needsAccessPassword && accessPassword.isEmpty))
            }
        }
        .padding(24)
        .frame(width: 420)
        .onAppear { isAddressFocused = true }
    }

    private func connect() {
        isConnecting = true
        error = nil
        Task {
            do {
                try await appState.addServer(address, accessPassword: needsAccessPassword ? accessPassword : nil)
                accessPassword = ""
                appState.showAddServer = false
            } catch RemoteServerError.accessPasswordRequired {
                needsAccessPassword = true
            } catch {
                self.error = error.localizedDescription
            }
            isConnecting = false
        }
    }
}

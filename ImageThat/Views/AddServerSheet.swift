import SwiftUI

/// Asks for the address of an imagethat-web server and checks it answers.
public struct AddServerSheet: View {
    @EnvironmentObject var appState: AppState
    @State private var address = ""
    @State private var error: String?
    @State private var isConnecting = false
    @FocusState private var isAddressFocused: Bool

    public init() {}

    public var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text("Connect to Server")
                .font(.title2)
                .fontWeight(.semibold)

            Text("Browse vaults on a machine running imagethat-web. The server decrypts, so only what you view crosses the network.")
                .font(.callout)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)

            TextField("https://nas.local:8347", text: $address)
                .textFieldStyle(.roundedBorder)
                .focused($isAddressFocused)
                .onSubmit { connect() }

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
                    .disabled(address.trimmingCharacters(in: .whitespaces).isEmpty || isConnecting)
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
                try await appState.addServer(address)
                appState.showAddServer = false
            } catch {
                self.error = error.localizedDescription
            }
            isConnecting = false
        }
    }
}

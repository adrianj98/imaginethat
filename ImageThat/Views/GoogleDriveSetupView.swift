import SwiftUI

public struct GoogleDriveSetupView: View {
    @EnvironmentObject var appState: AppState
    @State private var clientId = ""
    @State private var clientSecret = ""

    // Cache settings
    @State private var selectedLimit: Int64 = DriveCache.maxBytes

    private let limitOptions: [(label: String, bytes: Int64)] = [
        ("1 GB", 1_073_741_824),
        ("10 GB", 10_737_418_240),
        ("50 GB", 53_687_091_200),
        ("200 GB", 214_748_364_800),
        ("Unlimited", 0),
    ]

    @State private var cacheUsageBytes: Int64 = 0

    public init() {}

    public var body: some View {
        VStack(alignment: .leading, spacing: 20) {
            Text("Connect Google Drive")
                .font(.title2)
                .fontWeight(.semibold)

            VStack(alignment: .leading, spacing: 6) {
                Text("Create OAuth 2.0 credentials at console.cloud.google.com:")
                    .font(.callout)
                    .foregroundStyle(.secondary)
                VStack(alignment: .leading, spacing: 2) {
                    Text("1. APIs & Services → Credentials → Create Credentials")
                    Text("2. Choose **OAuth 2.0 Client ID** → Application type: **Desktop app**")
                    Text("3. No redirect URI registration required")
                }
                .font(.callout)
                .foregroundStyle(.secondary)
            }
            .fixedSize(horizontal: false, vertical: true)

            Divider()

            VStack(alignment: .leading, spacing: 10) {
                labeledField("Client ID", text: $clientId, placeholder: "Paste your Client ID")
                labeledField("Client Secret", text: $clientSecret, placeholder: "Paste your Client Secret", secure: true)
            }

            Divider()

            VStack(alignment: .leading, spacing: 8) {
                Text("Cache Limit")
                    .font(.headline)
                Picker("", selection: $selectedLimit) {
                    ForEach(limitOptions, id: \.bytes) { option in
                        Text(option.label).tag(option.bytes)
                    }
                }
                .pickerStyle(.segmented)
                .onChange(of: selectedLimit) { _, newValue in
                    DriveCache.maxBytes = newValue
                }

                HStack {
                    Text(cacheUsageLabel)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                    Spacer()
                    Button("Clear Cache") {
                        Task {
                            await DriveCache.shared.clear()
                            await refreshCacheUsage()
                        }
                    }
                    .font(.caption)
                }
            }

            Divider()

            HStack {
                Button("Cancel") {
                    appState.showGoogleDriveSetup = false
                }
                .keyboardShortcut(.escape, modifiers: [])

                Spacer()

                if appState.isConnectingGoogleDrive {
                    ProgressView()
                        .scaleEffect(0.7)
                        .padding(.trailing, 4)
                }

                Button("Connect") {
                    appState.connectGoogleDrive(clientId: clientId.trimmingCharacters(in: .whitespaces),
                                                clientSecret: clientSecret.trimmingCharacters(in: .whitespaces))
                }
                .keyboardShortcut(.return, modifiers: .command)
                .disabled(clientId.isEmpty || clientSecret.isEmpty || appState.isConnectingGoogleDrive)
            }
        }
        .padding(24)
        .frame(width: 480)
        .task { await refreshCacheUsage() }
    }

    @ViewBuilder
    private func labeledField(_ label: String, text: Binding<String>, placeholder: String, secure: Bool = false) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(label)
                .font(.subheadline)
                .fontWeight(.medium)
            if secure {
                SecureField(placeholder, text: text)
                    .textFieldStyle(.roundedBorder)
            } else {
                TextField(placeholder, text: text)
                    .textFieldStyle(.roundedBorder)
            }
        }
    }

    private var cacheUsageLabel: String {
        if cacheUsageBytes == 0 { return "No files cached" }
        let formatter = ByteCountFormatter()
        formatter.countStyle = .file
        return "Cache: \(formatter.string(fromByteCount: cacheUsageBytes))"
    }

    @MainActor
    private func refreshCacheUsage() async {
        cacheUsageBytes = await DriveCache.shared.currentSize()
    }
}

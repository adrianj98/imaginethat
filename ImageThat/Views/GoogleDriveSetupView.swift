import SwiftUI
import UniformTypeIdentifiers

public struct GoogleDriveSetupView: View {
    @EnvironmentObject var appState: AppState
    @State private var clientId = ""
    @State private var clientSecret = ""
    @State private var jsonImportError: String?
    @State private var showJSONImporter = false

    // Cache settings
    @State private var selectedLimit: Int64 = GocryptfsDriveCache.maxBytes

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
                HStack {
                    Text("Credentials")
                        .font(.headline)
                    Spacer()
                    Button {
                        showJSONImporter = true
                    } label: {
                        Label("Upload JSON…", systemImage: "doc.badge.arrow.up")
                    }
                    .controlSize(.small)
                }

                if let error = jsonImportError {
                    Text(error)
                        .font(.caption)
                        .foregroundStyle(.red)
                }

                labeledField("Client ID", text: $clientId, placeholder: "Paste your Client ID")
                labeledField("Client Secret", text: $clientSecret, placeholder: "Paste your Client Secret", secure: true)
            }
            .fileImporter(
                isPresented: $showJSONImporter,
                allowedContentTypes: [.json],
                allowsMultipleSelection: false
            ) { result in
                handleJSONImport(result)
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
                    GocryptfsDriveCache.maxBytes = newValue
                }

                HStack {
                    Text(cacheUsageLabel)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                    Spacer()
                    Button("Clear Cache") {
                        try? GocryptfsDriveCache.clear()
                        refreshCacheUsage()
                    }
                    .font(.caption)
                }
            }

            Divider()

            HStack {
                Button("Close") {
                    if appState.isConnectingGoogleDrive {
                        appState.cancelConnectGoogleDrive()
                    }
                    appState.showGoogleDriveSetup = false
                }
                .keyboardShortcut(.escape, modifiers: [])

                Spacer()

                if appState.isConnectingGoogleDrive {
                    ProgressView()
                        .scaleEffect(0.7)
                        .padding(.trailing, 4)
                    Button("Cancel Connection") {
                        appState.cancelConnectGoogleDrive()
                    }
                } else {
                    Button("Connect") {
                        appState.connectGoogleDrive(clientId: clientId.trimmingCharacters(in: .whitespaces),
                                                    clientSecret: clientSecret.trimmingCharacters(in: .whitespaces))
                    }
                    .keyboardShortcut(.return, modifiers: .command)
                    .disabled(clientId.isEmpty || clientSecret.isEmpty)
                }
            }
        }
        .padding(24)
        .frame(width: 480)
        .task {
            refreshCacheUsage()
            // Pre-fill from saved credentials so the user doesn't have to re-enter
            // them on every reconnect.
            if clientId.isEmpty {
                clientId = AppSettings.shared.string(forKey: "gdrive.clientId") ?? ""
            }
            if clientSecret.isEmpty {
                clientSecret = AppSettings.shared.string(forKey: "gdrive.clientSecret") ?? ""
            }
        }
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

    private func refreshCacheUsage() {
        cacheUsageBytes = GocryptfsDriveCache.size()
    }

    private func handleJSONImport(_ result: Result<[URL], Error>) {
        jsonImportError = nil
        switch result {
        case .success(let urls):
            guard let url = urls.first else { return }
            let needsAccess = url.startAccessingSecurityScopedResource()
            defer { if needsAccess { url.stopAccessingSecurityScopedResource() } }
            do {
                let data = try Data(contentsOf: url)
                try parseCredentialsJSON(data)
            } catch {
                jsonImportError = "Could not read file: \(error.localizedDescription)"
            }
        case .failure(let error):
            jsonImportError = error.localizedDescription
        }
    }

    private func parseCredentialsJSON(_ data: Data) throws {
        guard let root = try JSONSerialization.jsonObject(with: data) as? [String: Any] else {
            throw CredentialsError.invalidFormat
        }
        // Google's downloaded JSON wraps the values under "installed" or "web".
        let container: [String: Any]
        if let installed = root["installed"] as? [String: Any] {
            container = installed
        } else if let web = root["web"] as? [String: Any] {
            container = web
        } else {
            container = root
        }
        guard let id = container["client_id"] as? String, !id.isEmpty,
              let secret = container["client_secret"] as? String, !secret.isEmpty else {
            throw CredentialsError.missingFields
        }
        clientId = id
        clientSecret = secret
    }

    private enum CredentialsError: LocalizedError {
        case invalidFormat
        case missingFields

        var errorDescription: String? {
            switch self {
            case .invalidFormat: return "File is not valid JSON."
            case .missingFields: return "JSON is missing client_id or client_secret."
            }
        }
    }
}

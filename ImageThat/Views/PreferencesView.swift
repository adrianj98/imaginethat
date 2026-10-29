import SwiftUI

/// A macOS Settings view with a Shortcuts tab for remapping keyboard shortcuts.
public struct PreferencesView: View {
    public init() {}

    public var body: some View {
        TabView {
            ShortcutsTab()
                .tabItem {
                    Label("Shortcuts", systemImage: "keyboard")
                }
            SecurityTab()
                .tabItem {
                    Label("Security", systemImage: "lock.shield")
                }
            GoogleDriveTab()
                .tabItem {
                    Label("Google Drive", systemImage: "externaldrive.badge.icloud")
                }
        }
        .frame(width: 450, height: 400)
    }
}

// MARK: - Shortcuts Tab

public struct ShortcutsTab: View {
    @ObservedObject private var manager = ShortcutManager.shared

    public init() {}

    public var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            ForEach(ShortcutAction.allCases) { action in
                ShortcutRow(action: action, binding: manager.bindings[action] ?? ShortcutManager.defaultBinding(for: action))
                if action != ShortcutAction.allCases.last {
                    Divider()
                }
            }

            Spacer()

            HStack {
                Spacer()
                Button("Restore Defaults") {
                    manager.resetToDefaults()
                }
            }
            .padding(.top, 12)
        }
        .padding()
    }
}

// MARK: - Security Tab

public struct SecurityTab: View {
    @State private var savedVaults: [String] = []
    @State private var plaintextMode = PasswordStore.encryptionDisabled

    public init() {}

    public var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            VStack(alignment: .leading, spacing: 8) {
                Text("Password Storage")
                    .font(.headline)
                Toggle("Store passwords without encryption (debug mode)", isOn: $plaintextMode)
                    .onChange(of: plaintextMode) { _, newValue in
                        PasswordStore.encryptionDisabled = newValue
                    }
                Text("When disabled, passwords require Touch ID on first vault access.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }

            Divider()

            Text("Saved Vault Passwords")
                .font(.headline)

            if savedVaults.isEmpty {
                Text("No vault passwords saved.")
                    .foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity, alignment: .center)
                    .padding(.vertical, 20)
            } else {
                List {
                    ForEach(savedVaults, id: \.self) { path in
                        HStack {
                            VStack(alignment: .leading) {
                                Text(URL(fileURLWithPath: path).lastPathComponent)
                                    .fontWeight(.medium)
                                Text(path)
                                    .font(.caption)
                                    .foregroundStyle(.secondary)
                                    .lineLimit(1)
                                    .truncationMode(.middle)
                            }
                            Spacer()
                            Button("Forget") {
                                forgetVault(path: path)
                            }
                            .foregroundStyle(.red)
                        }
                    }
                }
                .listStyle(.bordered)
            }

            Divider()

            Text("Thumbnail Cache")
                .font(.headline)

            HStack {
                Text(cacheSizeLabel)
                    .foregroundStyle(.secondary)
                Spacer()
                Button("Clear Cache") {
                    try? GocryptfsThumbnails.clearCache(at: AppPaths.thumbnailCache)
                    updateCacheSize()
                }
            }

            Spacer()

            HStack {
                Spacer()
                Button("Forget All Passwords") {
                    PasswordStore.shared.deleteAll()
                    KeychainManager.shared.deleteMasterKey()
                    savedVaults = []
                }
                .disabled(savedVaults.isEmpty)
            }
        }
        .padding()
        .onAppear {
            loadSavedVaults()
            updateCacheSize()
        }
    }

    private func loadSavedVaults() {
        guard let key = try? KeychainManager.shared.getMasterKey() else {
            savedVaults = []
            return
        }
        savedVaults = PasswordStore.shared.savedVaultPaths(masterKey: key)
    }

    private func forgetVault(path: String) {
        if let key = try? KeychainManager.shared.getMasterKey() {
            try? PasswordStore.shared.delete(vault: path, masterKey: key)
        }
        savedVaults.removeAll { $0 == path }
    }

    private var cacheSizeLabel: String {
        let bytes = cacheBytes
        if bytes == 0 { return "No cached thumbnails" }
        let formatter = ByteCountFormatter()
        return "Cache: \(formatter.string(fromByteCount: bytes))"
    }

    @State private var cacheBytes: Int64 = 0

    private func updateCacheSize() {
        cacheBytes = GocryptfsThumbnails.cacheSize(at: AppPaths.thumbnailCache)
    }
}

// MARK: - Google Drive Tab

public struct GoogleDriveTab: View {
    private let limitOptions: [(label: String, bytes: Int64)] = [
        ("1 GB", 1_073_741_824),
        ("10 GB", 10_737_418_240),
        ("50 GB", 53_687_091_200),
        ("200 GB", 214_748_364_800),
        ("Unlimited", 0),
    ]

    @State private var selectedLimit: Int64 = DriveCache.maxBytes
    @State private var cacheBytes: Int64 = 0

    public init() {}

    public var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text("Drive Cache")
                .font(.headline)

            Picker("Maximum cache size", selection: $selectedLimit) {
                ForEach(limitOptions, id: \.bytes) { option in
                    Text(option.label).tag(option.bytes)
                }
            }
            .onChange(of: selectedLimit) { _, newValue in
                DriveCache.maxBytes = newValue
            }

            HStack {
                Text(cacheLabel)
                    .foregroundStyle(.secondary)
                Spacer()
                Button("Clear Cache") {
                    Task {
                        await DriveCache.shared.clear()
                        cacheBytes = await DriveCache.shared.currentSize()
                    }
                }
            }

            Spacer()
        }
        .padding()
        .task {
            selectedLimit = DriveCache.maxBytes
            cacheBytes = await DriveCache.shared.currentSize()
        }
    }

    private var cacheLabel: String {
        if cacheBytes == 0 { return "No files cached" }
        let f = ByteCountFormatter()
        f.countStyle = .file
        return "Cache: \(f.string(fromByteCount: cacheBytes))"
    }
}

// MARK: - Single shortcut row

public struct ShortcutRow: View {
    public let action: ShortcutAction
    public let binding: KeyBinding

    @State private var isRecording = false

    public init(action: ShortcutAction, binding: KeyBinding) {
        self.action = action
        self.binding = binding
    }

    public var body: some View {
        HStack {
            Text(action.displayName)
                .frame(maxWidth: .infinity, alignment: .leading)

            KeyRecorderButton(
                binding: binding,
                isRecording: $isRecording,
                onRecord: { newBinding in
                    ShortcutManager.shared.updateBinding(for: action, to: newBinding)
                }
            )
            .frame(width: 160)
        }
        .padding(.vertical, 6)
    }
}

// MARK: - Key Recorder

/// A button that, when clicked, listens for the next key press and records it
/// as a new shortcut binding.
public struct KeyRecorderButton: View {
    public let binding: KeyBinding
    @Binding public var isRecording: Bool
    public let onRecord: (KeyBinding) -> Void

    public init(binding: KeyBinding, isRecording: Binding<Bool>, onRecord: @escaping (KeyBinding) -> Void) {
        self.binding = binding
        self._isRecording = isRecording
        self.onRecord = onRecord
    }

    public var body: some View {
        Button {
            isRecording = true
        } label: {
            Text(isRecording ? "Press a key..." : binding.displayString)
                .frame(maxWidth: .infinity)
                .padding(.vertical, 4)
                .padding(.horizontal, 8)
                .background(
                    RoundedRectangle(cornerRadius: 6)
                        .fill(isRecording ? Color.accentColor.opacity(0.15) : Color(nsColor: .controlBackgroundColor))
                )
                .overlay(
                    RoundedRectangle(cornerRadius: 6)
                        .stroke(isRecording ? Color.accentColor : Color.gray.opacity(0.3), lineWidth: 1)
                )
        }
        .buttonStyle(.plain)
        .focusable(isRecording)
        .onKeyPress(phases: .down) { keyPress in
            guard isRecording else { return .ignored }
            let name = ShortcutManager.keyName(from: keyPress.key)
            let mods = Int(keyPress.modifiers.rawValue)
            let newBinding = KeyBinding(keyName: name, modifiers: mods)
            onRecord(newBinding)
            isRecording = false
            return .handled
        }
    }
}

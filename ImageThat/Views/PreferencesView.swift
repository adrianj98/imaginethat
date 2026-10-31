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
        }
        .frame(width: 600, height: 500)
        .background(Glass.lightCanvas)
    }
}

/// White rounded group for settings rows, with an optional heading above.
struct SettingsGroup<Content: View>: View {
    var title: String?
    @ViewBuilder var content: Content

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            if let title {
                SectionHeading(title: title).padding(.leading, 12)
            }
            VStack(spacing: 0) { content }
                .background(RoundedRectangle(cornerRadius: 14, style: .continuous).fill(Color(nsColor: .controlBackgroundColor)))
                .overlay(RoundedRectangle(cornerRadius: 14, style: .continuous).strokeBorder(Color.black.opacity(0.08), lineWidth: 0.5))
        }
    }
}

/// One 44pt settings row: title, optional subtitle, trailing controls.
struct SettingsRow<Trailing: View>: View {
    let title: String
    var subtitle: String?
    var subtitleColor: Color = Glass.label
    @ViewBuilder var trailing: Trailing

    var body: some View {
        HStack(spacing: 12) {
            VStack(alignment: .leading, spacing: 1) {
                Text(title).font(.system(size: 13))
                if let subtitle {
                    Text(subtitle).font(.system(size: 11)).foregroundStyle(subtitleColor)
                        .lineLimit(1).truncationMode(.middle)
                }
            }
            Spacer(minLength: 8)
            trailing
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 6)
        .frame(minHeight: 44)
    }
}

// MARK: - Shortcuts Tab

public struct ShortcutsTab: View {
    @ObservedObject private var manager = ShortcutManager.shared

    public init() {}

    public var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            SettingsGroup(title: "Click a key and press a new one to change it") {
                ForEach(ShortcutAction.allCases) { action in
                    ShortcutRow(action: action, binding: manager.bindings[action] ?? ShortcutManager.defaultBinding(for: action))
                        .padding(.horizontal, 14)
                    if action != ShortcutAction.allCases.last {
                        Divider().padding(.leading, 14)
                    }
                }
            }

            HStack {
                Spacer()
                Button("Restore Defaults") {
                    manager.resetToDefaults()
                }
                .buttonStyle(.link)
            }
            .padding(.horizontal, 12)

            Spacer()
        }
        .padding(24)
    }
}

// MARK: - Security Tab

public struct SecurityTab: View {
    @State private var savedVaults: [String] = []
    @State private var plaintextMode = PasswordStore.encryptionDisabled

    public init() {}

    public var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 18) {
                VStack(alignment: .leading, spacing: 8) {
                    SettingsGroup(title: "Saved vault passwords") {
                        if savedVaults.isEmpty {
                            SettingsRow(title: "No vault passwords saved.") { EmptyView() }
                                .foregroundStyle(.secondary)
                        }
                        ForEach(savedVaults, id: \.self) { path in
                            HStack(spacing: 12) {
                                Image(systemName: "lock.shield")
                                    .foregroundStyle(Color.accentColor)
                                SettingsRow(title: URL(fileURLWithPath: path).lastPathComponent, subtitle: path) {
                                    Button("Forget") {
                                        forgetVault(path: path)
                                    }
                                    .buttonStyle(PillButtonStyle(height: 28))
                                    .font(.system(size: 12))
                                }
                                .padding(.leading, -14)
                            }
                            .padding(.leading, 14)
                            if path != savedVaults.last {
                                Divider().padding(.leading, 44)
                            }
                        }
                    }
                    HStack {
                        Text("Encrypted with a master key in the Keychain, behind Touch ID.")
                            .font(.system(size: 11))
                            .foregroundStyle(Glass.label)
                        Spacer()
                        Button("Forget All") {
                            PasswordStore.shared.deleteAll()
                            KeychainManager.shared.deleteMasterKey()
                            savedVaults = []
                        }
                        .buttonStyle(.link)
                        .disabled(savedVaults.isEmpty)
                    }
                    .padding(.horizontal, 12)
                }

                SettingsGroup(title: "Thumbnail cache") {
                    SettingsRow(title: cacheSizeLabel, subtitle: "Encrypted with each vault's own key") {
                        Button("Clear") {
                            try? GocryptfsThumbnails.clearCache(at: AppPaths.thumbnailCache)
                            updateCacheSize()
                        }
                        .buttonStyle(PillButtonStyle(height: 28))
                        .font(.system(size: 12))
                    }
                }

                NetworkCacheGroup()

                SettingsGroup {
                    SettingsRow(title: "Store passwords unencrypted",
                                subtitle: "Debug only — saved passwords become readable on disk.",
                                subtitleColor: Color(red: 0xB3 / 255, green: 0x26 / 255, blue: 0x1E / 255)) {
                        Toggle("Store passwords unencrypted", isOn: $plaintextMode)
                            .labelsHidden()
                            .toggleStyle(.switch)
                            .controlSize(.small)
                            .onChange(of: plaintextMode) { _, newValue in
                                PasswordStore.encryptionDisabled = newValue
                            }
                    }
                }
            }
            .padding(24)
        }
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
        return "\(formatter.string(fromByteCount: bytes)) on disk"
    }

    @State private var cacheBytes: Int64 = 0

    private func updateCacheSize() {
        cacheBytes = GocryptfsThumbnails.cacheSize(at: AppPaths.thumbnailCache)
    }
}

// MARK: - Network cache

/// Size limit and usage of the one cache every network transport shares.
struct NetworkCacheGroup: View {
    private let limitOptions: [(label: String, bytes: Int64)] = [
        ("1 GB", 1_073_741_824),
        ("10 GB", 10_737_418_240),
        ("50 GB", 53_687_091_200),
        ("200 GB", 214_748_364_800),
        ("Unlimited", 0),
    ]

    @State private var selectedLimit: Int64 = GocryptfsNetworkCache.maxBytes
    @State private var cacheBytes: Int64 = 0

    var body: some View {
        SettingsGroup(title: "Network cache") {
                SettingsRow(title: cacheLabel,
                            subtitle: "What Google Drive, servers and network folders have sent. Stays encrypted.") {
                    Picker("Maximum", selection: $selectedLimit) {
                        ForEach(limitOptions, id: \.bytes) { option in
                            Text(option.label).tag(option.bytes)
                        }
                    }
                    .font(.system(size: 12))
                    .fixedSize()
                    .onChange(of: selectedLimit) { _, newValue in
                        GocryptfsNetworkCache.maxBytes = newValue
                    }
                    Button("Clear") {
                        try? GocryptfsNetworkCache.clear()
                        cacheBytes = GocryptfsNetworkCache.size()
                    }
                    .buttonStyle(PillButtonStyle(height: 28))
                    .font(.system(size: 12))
                }
        }
        .onAppear {
            selectedLimit = GocryptfsNetworkCache.maxBytes
            cacheBytes = GocryptfsNetworkCache.size()
        }
    }

    private var cacheLabel: String {
        if cacheBytes == 0 { return "No files cached" }
        let f = ByteCountFormatter()
        f.countStyle = .file
        return "\(f.string(fromByteCount: cacheBytes)) on disk"
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
            .frame(width: 120)
        }
        .frame(minHeight: 44)
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
                .font(.system(size: 12, weight: .medium))
                .monospacedDigit()
                .frame(maxWidth: .infinity)
                .frame(height: 26)
                .padding(.horizontal, 10)
                .background(
                    Capsule()
                        .fill(isRecording ? Color.accentColor.opacity(0.15) : Color.black.opacity(0.06))
                )
                .overlay(
                    Capsule()
                        .stroke(isRecording ? Color.accentColor : Color.clear, lineWidth: 1)
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

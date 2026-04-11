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

    public init() {}

    public var body: some View {
        VStack(alignment: .leading, spacing: 16) {
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
                                try? KeychainManager.shared.deletePassword(forVault: path)
                                savedVaults.removeAll { $0 == path }
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
                    PersistentThumbnailCache.shared.clearAll()
                    updateCacheSize()
                }
            }

            Spacer()

            HStack {
                Spacer()
                Button("Forget All Passwords") {
                    for path in savedVaults {
                        try? KeychainManager.shared.deletePassword(forVault: path)
                    }
                    savedVaults = []
                }
                .disabled(savedVaults.isEmpty)
            }
        }
        .padding()
        .onAppear {
            savedVaults = KeychainManager.shared.savedVaultPaths()
            updateCacheSize()
        }
    }

    private var cacheSizeLabel: String {
        let bytes = cacheBytes
        if bytes == 0 { return "No cached thumbnails" }
        let formatter = ByteCountFormatter()
        return "Cache: \(formatter.string(fromByteCount: bytes))"
    }

    @State private var cacheBytes: Int64 = 0

    private func updateCacheSize() {
        cacheBytes = PersistentThumbnailCache.shared.cacheSize()
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

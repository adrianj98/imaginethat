import SwiftUI

/// Identifiers for all remappable keyboard actions.
public enum ShortcutAction: String, CaseIterable, Identifiable {
    case nextImage
    case previousImage
    case backToGrid
    case togglePlayPause
    case openFolder
    case goUp

    public var id: String { rawValue }

    public var displayName: String {
        switch self {
        case .nextImage:        return "Next Image"
        case .previousImage:    return "Previous Image"
        case .backToGrid:       return "Back to Grid"
        case .togglePlayPause:  return "Play / Pause Video"
        case .openFolder:       return "Open Folder"
        case .goUp:             return "Go Up Directory"
        }
    }

    /// The UserDefaults key prefix for this action.
    fileprivate var defaultsPrefix: String { "shortcut_\(rawValue)" }
}

/// A serialisable representation of a key combination.
public struct KeyBinding: Equatable, Codable {
    /// The key equivalent string (e.g. "a", or a special constant like
    /// KeyEquivalent.rightArrow represented by its raw unicode scalar).
    public var keyName: String
    /// Raw values of EventModifiers (shift = 1 << 17, control = 1 << 18,
    /// option = 1 << 19, command = 1 << 20 in SwiftUI).
    public var modifiers: Int

    public init(keyName: String, modifiers: Int = 0) {
        self.keyName = keyName
        self.modifiers = modifiers
    }

    // MARK: - Display

    public var displayString: String {
        var parts: [String] = []
        let mods = EventModifiers(rawValue: modifiers)
        if mods.contains(.control) { parts.append("⌃") }
        if mods.contains(.option)  { parts.append("⌥") }
        if mods.contains(.shift)   { parts.append("⇧") }
        if mods.contains(.command) { parts.append("⌘") }

        let keyLabel: String
        switch keyName {
        case "rightArrow":  keyLabel = "→"
        case "leftArrow":   keyLabel = "←"
        case "upArrow":     keyLabel = "↑"
        case "downArrow":   keyLabel = "↓"
        case "escape":      keyLabel = "⎋"
        case "space":       keyLabel = "Space"
        case "return":      keyLabel = "↩"
        case "delete":      keyLabel = "⌫"
        case "tab":         keyLabel = "⇥"
        default:            keyLabel = keyName.uppercased()
        }
        parts.append(keyLabel)
        return parts.joined()
    }
}

/// Manages user-configurable keyboard shortcuts, persisted in UserDefaults.
@MainActor
public class ShortcutManager: ObservableObject {
    public static let shared = ShortcutManager()

    @Published public var bindings: [ShortcutAction: KeyBinding]

    private init() {
        var loaded: [ShortcutAction: KeyBinding] = [:]
        for action in ShortcutAction.allCases {
            if let data = UserDefaults.standard.data(forKey: action.defaultsPrefix),
               let binding = try? JSONDecoder().decode(KeyBinding.self, from: data) {
                loaded[action] = binding
            } else {
                loaded[action] = Self.defaultBinding(for: action)
            }
        }
        self.bindings = loaded
    }

    // MARK: - Defaults

    public static func defaultBinding(for action: ShortcutAction) -> KeyBinding {
        switch action {
        case .nextImage:        return KeyBinding(keyName: "rightArrow")
        case .previousImage:    return KeyBinding(keyName: "leftArrow")
        case .backToGrid:       return KeyBinding(keyName: "escape")
        case .togglePlayPause:  return KeyBinding(keyName: "space")
        case .openFolder:       return KeyBinding(keyName: "o", modifiers: Int(EventModifiers.command.rawValue))
        case .goUp:             return KeyBinding(keyName: "upArrow", modifiers: Int(EventModifiers.command.rawValue))
        }
    }

    // MARK: - Persistence

    public func updateBinding(for action: ShortcutAction, to binding: KeyBinding) {
        bindings[action] = binding
        if let data = try? JSONEncoder().encode(binding) {
            UserDefaults.standard.set(data, forKey: action.defaultsPrefix)
        }
    }

    public func resetToDefaults() {
        for action in ShortcutAction.allCases {
            let binding = Self.defaultBinding(for: action)
            bindings[action] = binding
            UserDefaults.standard.removeObject(forKey: action.defaultsPrefix)
        }
    }

    // MARK: - Matching

    /// Returns the key name string for a SwiftUI `KeyPress`.
    public static func keyName(from key: KeyEquivalent) -> String {
        switch key {
        case .rightArrow:   return "rightArrow"
        case .leftArrow:    return "leftArrow"
        case .upArrow:      return "upArrow"
        case .downArrow:    return "downArrow"
        case .escape:       return "escape"
        case .space:        return "space"
        case .return:       return "return"
        case .delete:       return "delete"
        case .tab:          return "tab"
        default:            return String(key.character)
        }
    }

    /// Check whether a `KeyPress` event matches a given action.
    public func matches(_ keyPress: KeyPress, action: ShortcutAction) -> Bool {
        guard let binding = bindings[action] else { return false }
        let name = Self.keyName(from: keyPress.key)
        let mods = Int(keyPress.modifiers.rawValue)
        return name == binding.keyName && mods == binding.modifiers
    }
}

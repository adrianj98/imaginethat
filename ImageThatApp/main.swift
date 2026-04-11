import SwiftUI
import AppKit
import ImageThatCore

/// Holds the initial folder path from env or command line
enum LaunchArgs {
    static var initialPath: URL? = {
        // Check environment variable first (used by `make run FOLDER=...`)
        if let envPath = ProcessInfo.processInfo.environment["IMAGETHAT_OPEN"], !envPath.isEmpty {
            return URL(fileURLWithPath: (envPath as NSString).expandingTildeInPath)
        }
        // Fall back to command line argument
        let args = CommandLine.arguments
        guard args.count > 1 else { return nil }
        return URL(fileURLWithPath: (args[1] as NSString).expandingTildeInPath)
    }()
}

@main
struct ImageThatApp: App {
    @StateObject private var appState = AppState()

    init() {
        NSApplication.shared.setActivationPolicy(.regular)
        NSApplication.shared.activate(ignoringOtherApps: true)
    }

    var body: some Scene {
        WindowGroup {
            ContentView()
                .environmentObject(appState)
                .onAppear {
                    if let url = LaunchArgs.initialPath {
                        LaunchArgs.initialPath = nil
                        DispatchQueue.main.asyncAfter(deadline: .now() + 0.5) {
                            appState.openDirectory(url: url)
                        }
                    }
                }
        }
        .commands {
            CommandGroup(replacing: .newItem) {
                Button("Open Folder...") {
                    appState.openFolder()
                }
                .keyboardShortcut("o", modifiers: .command)
            }
        }

        Settings {
            PreferencesView()
        }
    }
}

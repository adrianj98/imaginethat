import SwiftUI
import AppKit
import ImageThatCore

@main
struct ImageThatApp: App {
    @StateObject private var appState = AppState()
    @State private var hasOpenedInitialPath = false

    init() {
        NSApplication.shared.setActivationPolicy(.regular)
        NSApplication.shared.activate(ignoringOtherApps: true)
    }

    var body: some Scene {
        WindowGroup {
            ContentView()
                .environmentObject(appState)
                .task {
                    guard !hasOpenedInitialPath else { return }
                    hasOpenedInitialPath = true

                    // Open folder from command line argument
                    let args = CommandLine.arguments
                    guard args.count > 1 else { return }
                    let path = (args[1] as NSString).expandingTildeInPath
                    let url = URL(fileURLWithPath: path)

                    try? await Task.sleep(for: .seconds(1))
                    appState.openDirectory(url: url)
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

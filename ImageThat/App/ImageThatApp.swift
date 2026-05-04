import SwiftUI

@main
struct ImageThatApp: App {
    @StateObject private var appState = AppState()

    var body: some Scene {
        WindowGroup {
            ContentView()
                .environmentObject(appState)
                .onAppear { appState.restoreSession() }
                .onOpenURL { url in
                    appState.openFile(url: url)
                }
        }
        .commands {
            CommandGroup(replacing: .newItem) {
                Button("Open Folder...") {
                    appState.openFolder()
                }
                .keyboardShortcut("o", modifiers: .command)

                Button("Open Encrypted Vault...") {
                    appState.openVault()
                }
                .keyboardShortcut("o", modifiers: [.command, .shift])
            }
        }
    }
}

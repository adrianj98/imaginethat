import AppKit

public class AppDelegate: NSObject, NSApplicationDelegate {
    /// Set once the app's state exists, so quitting can see running copies.
    @MainActor public var transfers: TransferCenter?

    public func application(_ application: NSApplication, open urls: [URL]) {
        for url in urls {
            NotificationCenter.default.post(name: Notification.Name("com.adrianm.imagethat.openExternalFile"), object: url)
        }
    }

    /// Copies can't continue after a quit, so ask first, and let the ones
    /// being stopped clean up their temp files before the process goes.
    @MainActor public func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        guard let transfers, transfers.hasRunningCopies else { return .terminateNow }
        let alert = NSAlert()
        alert.messageText = "Copies are still running"
        alert.informativeText = "Quitting stops them. Files already copied stay, and importing the same files again skips those."
        alert.addButton(withTitle: "Keep Copying")
        alert.addButton(withTitle: "Quit Anyway")
        guard alert.runModal() == .alertSecondButtonReturn else { return .terminateCancel }

        transfers.cancelAll()
        Task { @MainActor in
            for _ in 0..<30 where transfers.hasRunningCopies {
                try? await Task.sleep(for: .milliseconds(100))
            }
            sender.reply(toApplicationShouldTerminate: true)
        }
        return .terminateLater
    }
}

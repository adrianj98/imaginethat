import AppKit

public class AppDelegate: NSObject, NSApplicationDelegate {
    public func application(_ application: NSApplication, open urls: [URL]) {
        for url in urls {
            NotificationCenter.default.post(name: Notification.Name("com.adrianm.imagethat.openExternalFile"), object: url)
        }
    }
}

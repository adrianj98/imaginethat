import AppKit
import SwiftUI
import ImageThatCore

/// Drives the real DetailView in a window with synthetic clicks and keys, and
/// saves renders of what it shows. Not part of `make test-logic`: it opens a
/// window, takes focus and goes fullscreen.
///
/// What it can judge: double-click zoom (images and videos), the 0, F and Esc
/// keys, fullscreen. What it can't: drags and scrolling. Synthetic drags don't
/// reach SwiftUI's drag gestures (not even on a bare view), and synthetic
/// scrolls arrive without a window. Those need a person. Video frames don't
/// show in the renders either, so it prints the player layer's frame instead.
///
///     IntegrationTest viewer-ui <folder with one image then one video> <output folder>
@MainActor
enum ViewerUITest {
    static var window: NSWindow!
    static var host: NSView!
    static var out: URL!
    static var eventNumber = 0

    static func pause(_ seconds: Double) async {
        try? await Task.sleep(for: .seconds(seconds))
    }

    /// Mouse events take window coordinates (origin bottom left); these
    /// helpers take points measured from the top left, like SwiftUI.
    static func mouse(_ type: NSEvent.EventType, _ p: CGPoint, clicks: Int = 1) {
        eventNumber += 1
        let location = CGPoint(x: p.x, y: host.bounds.height - p.y)
        let event = NSEvent.mouseEvent(with: type, location: location, modifierFlags: [],
                                       timestamp: ProcessInfo.processInfo.systemUptime,
                                       windowNumber: window.windowNumber, context: nil,
                                       eventNumber: eventNumber, clickCount: clicks, pressure: 1)!
        NSApp.postEvent(event, atStart: false) // queued, so gesture tracking loops see it
    }

    static func doubleClick(_ p: CGPoint) async {
        mouse(.leftMouseDown, p, clicks: 1); mouse(.leftMouseUp, p, clicks: 1)
        await pause(0.05)
        mouse(.leftMouseDown, p, clicks: 2); mouse(.leftMouseUp, p, clicks: 2)
        await pause(0.6)
    }

    /// A drag built from Core Graphics events, which carry the movement
    /// deltas SwiftUI's drag gesture reads; plain NSEvents don't.
    static func cgMouse(_ type: CGEventType, _ p: CGPoint, delta: CGSize = .zero) {
        let onScreen = window.convertPoint(toScreen: CGPoint(x: p.x, y: host.bounds.height - p.y))
        let screenHeight = NSScreen.screens.first?.frame.height ?? 0
        guard let cg = CGEvent(mouseEventSource: nil, mouseType: type,
                               mouseCursorPosition: CGPoint(x: onScreen.x, y: screenHeight - onScreen.y),
                               mouseButton: .left) else { return }
        cg.setIntegerValueField(.mouseEventWindowUnderMousePointer, value: Int64(window.windowNumber))
        cg.setIntegerValueField(.mouseEventWindowUnderMousePointerThatCanHandleThisEvent,
                                value: Int64(window.windowNumber))
        cg.setIntegerValueField(.mouseEventClickState, value: 1)
        cg.setDoubleValueField(.mouseEventDeltaX, value: Double(delta.width))
        cg.setDoubleValueField(.mouseEventDeltaY, value: Double(delta.height))
        if let event = NSEvent(cgEvent: cg) { NSApp.postEvent(event, atStart: false) }
    }

    static func drag(from a: CGPoint, by d: CGSize) async {
        cgMouse(.leftMouseDown, a)
        await pause(0.05)
        for step in 1...10 {
            let t = CGFloat(step) / 10
            cgMouse(.leftMouseDragged, CGPoint(x: a.x + d.width * t, y: a.y + d.height * t),
                    delta: CGSize(width: d.width / 10, height: d.height / 10))
            await pause(0.03)
        }
        cgMouse(.leftMouseUp, CGPoint(x: a.x + d.width, y: a.y + d.height))
        await pause(0.4)
    }

    static func key(_ characters: String, code: UInt16) async {
        for type in [NSEvent.EventType.keyDown, .keyUp] {
            let event = NSEvent.keyEvent(with: type, location: .zero, modifierFlags: [],
                                         timestamp: ProcessInfo.processInfo.systemUptime,
                                         windowNumber: window.windowNumber, context: nil,
                                         characters: characters, charactersIgnoringModifiers: characters,
                                         isARepeat: false, keyCode: code)!
            NSApp.postEvent(event, atStart: false)
        }
        await pause(0.5)
    }

    /// A scroll at `p`, routed by the window the way a real one is. Returns
    /// the AppKit view under the point.
    static func scroll(at p: CGPoint, deltaY: Int32) async -> String {
        let inWindow = CGPoint(x: p.x, y: host.bounds.height - p.y)
        let target = host.hitTest(inWindow)
        let name = target.map { NSStringFromClass(type(of: $0)) } ?? "nil"
        let onScreen = window.convertPoint(toScreen: inWindow)
        let screenHeight = NSScreen.screens.first?.frame.height ?? 0
        if let cg = CGEvent(scrollWheelEvent2Source: nil, units: .pixel, wheelCount: 1,
                            wheel1: deltaY, wheel2: 0, wheel3: 0) {
            cg.location = CGPoint(x: onScreen.x, y: screenHeight - onScreen.y)
            cg.setIntegerValueField(.mouseEventWindowUnderMousePointer, value: Int64(window.windowNumber))
            cg.setIntegerValueField(.mouseEventWindowUnderMousePointerThatCanHandleThisEvent,
                                    value: Int64(window.windowNumber))
            // Through the window, as AppKit routes a real scroll: to the view under the pointer.
            if let event = NSEvent(cgEvent: cg) {
                window.sendEvent(event)
            }
        }
        await pause(0.4)
        return String(name.prefix(40))
    }

    static func shot(_ name: String) {
        guard let rep = host.bitmapImageRepForCachingDisplay(in: host.bounds) else { return }
        host.cacheDisplay(in: host.bounds, to: rep)
        try? rep.representation(using: .png, properties: [:])?
            .write(to: out.appendingPathComponent(name + ".png"))
    }

    static func find<T: NSView>(_ type: T.Type, in view: NSView) -> T? {
        if let v = view as? T { return v }
        for sub in view.subviews { if let v = find(type, in: sub) { return v } }
        return nil
    }

    /// The video picture's frame inside its view, as "x y w h" from the top left.
    static func picture() -> String {
        guard let view = find(PlayerLayerView.self, in: host), let layer = view.layer?.sublayers?.first else {
            return "no player"
        }
        let f = layer.frame
        let top = (view.isFlipped || view.layer?.isGeometryFlipped == true) ? f.minY : view.bounds.height - f.maxY
        return "x \(Int(f.minX)) y \(Int(top)) w \(Int(f.width)) h \(Int(f.height)) in \(Int(view.bounds.width))x\(Int(view.bounds.height))"
    }

    /// Runs the script inside a real AppKit event loop: without one, the
    /// first mouse event stops the bare run loop under Swift's async main.
    static func run(folder: String, output: String) {
        let app = NSApplication.shared
        app.setActivationPolicy(.regular)
        Task { @MainActor in
            await script(folder: folder, output: output)
            exit(0)
        }
        app.run()
    }

    static func script(folder: String, output: String) async {
        out = URL(fileURLWithPath: output)
        try? FileManager.default.createDirectory(at: out, withIntermediateDirectories: true)
        func say(_ s: String) { print(s); fflush(stdout) }

        let app = NSApplication.shared
        let appState = AppState()
        appState.openDirectory(url: URL(fileURLWithPath: folder))
        for _ in 0..<50 where appState.mediaItems.count < 2 { await pause(0.1) }
        say("items: \(appState.mediaItems.map(\.name))")

        window = NSWindow(contentRect: NSRect(x: 100, y: 100, width: 1000, height: 700),
                          styleMask: [.titled, .closable, .resizable], backing: .buffered, defer: false)
        window.collectionBehavior = [.fullScreenPrimary]
        let hosting = NSHostingView(rootView: DetailView().environmentObject(appState))
        window.contentView = hosting
        host = hosting
        window.makeKeyAndOrderFront(nil)
        app.activate(ignoringOtherApps: true)

        appState.enterDetail(at: 0)
        await pause(1.5)

        // Stage: 76 from the top, 32 from the left, 316 from the right (info card), 16 from the bottom.
        let centre = CGPoint(x: 32 + (1000 - 32 - 316) / 2, y: 76 + (700 - 76 - 16) / 2)

        // Keys only reach the viewer in a key window, and macOS may refuse to
        // activate an app started from a terminal while another one is in use.
        for _ in 0..<20 where !window.isKeyWindow {
            NSRunningApplication.current.activate(options: [.activateAllWindows, .activateIgnoringOtherApps])
            window.orderFrontRegardless()
            window.makeKey()
            await pause(0.3)
        }
        say("window key \(window.isKeyWindow), main \(window.isMainWindow), app active \(app.isActive)")

        // --- Image ---
        shot("01-image-fit")
        await doubleClick(CGPoint(x: centre.x + 150, y: centre.y - 100))
        shot("02-image-double-click")
        await drag(from: centre, by: CGSize(width: 120, height: 80))
        shot("03-image-dragged-right-down")
        await key("0", code: 29)
        shot("04-image-key-0")
        say("image scroll goes to: \(await scroll(at: centre, deltaY: -40))")
        shot("05-image-scrolled")
        await doubleClick(centre)
        shot("06-image-double-click-back")

        await key("f", code: 3)
        await pause(3)
        say("F on the image: fullscreen \(window.styleMask.contains(.fullScreen))")
        if window.styleMask.contains(.fullScreen) {
            await key("f", code: 3)
            await pause(3)
            say("F again: fullscreen \(window.styleMask.contains(.fullScreen))")
        }

        // --- Video ---
        // A swipe left at fit goes to the next item (the viewer's existing drag).
        await drag(from: centre, by: CGSize(width: -220, height: 0))
        say("after swipe left at fit: item \(appState.selectedIndex)")
        if appState.selectedIndex == 0 { appState.navigateNext() }
        await pause(2)
        say("on item \(appState.selectedIndex): \(appState.selectedItem?.name ?? "-")")
        say("video at fit:            \(picture())")
        await doubleClick(CGPoint(x: centre.x + 100, y: centre.y - 60))
        say("video double-clicked:    \(picture())")
        shot("07-video-double-click")
        await drag(from: centre, by: CGSize(width: 100, height: 50))
        say("video dragged +100,+50:  \(picture())")
        await key("0", code: 29)
        say("video key 0:             \(picture())")
        say("video scroll goes to: \(await scroll(at: centre, deltaY: -40))")
        say("video scrolled:          \(picture())")
        shot("08-video-scrolled")
        await key("0", code: 29)

        // --- Fullscreen ---
        func full() -> Bool { window.styleMask.contains(.fullScreen) }
        await key("f", code: 3)
        await pause(2.5)
        say("after F: fullscreen \(full())")
        if !full() {
            window.toggleFullScreen(nil)
            await pause(2.5)
            say("control, toggleFullScreen called directly: fullscreen \(full())")
        }
        shot("09-fullscreen")
        await key("\u{1b}", code: 53)
        await pause(4)
        say("after Esc: fullscreen \(full()), view \(appState.viewMode)")
        await key("\u{1b}", code: 53)
        say("after second Esc: view \(appState.viewMode)")
        shot("10-end")
        window.close()
    }
}

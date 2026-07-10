import AppKit
import SwiftUI
import Testing
import AtelierCodeAPI
@testable import AtelierCode

/// Hosts the full window shell (NavigationSplitView + sidebar List +
/// searchable + detail) in a real window and pumps the run loop. This is the
/// hierarchy the app boots into, so it's where launch-time AppKit warnings
/// (reentrant NSTableView delegate operations, invalid Picker selections)
/// surface under a controlled model instead of the developer's live state.
@Suite("Shell hosting smoke")
struct ShellHostingSmokeTest {
    private func pump(seconds: TimeInterval) {
        RunLoop.main.run(until: Date(timeIntervalSinceNow: seconds))
    }

    private func host(_ view: some View) {
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 900, height: 600),
            styleMask: [.titled], backing: .buffered, defer: false
        )
        window.contentView = NSHostingView(rootView: view)
        window.orderFront(nil)
        pump(seconds: 0.8)
        window.orderOut(nil)
    }

    private func waitForData(_ runtime: ConnectionRuntime) async {
        for _ in 0..<100 {
            if !runtime.projects.projects.isEmpty && !runtime.sessions.sessions.isEmpty { return }
            try? await Task.sleep(for: .milliseconds(20))
        }
    }

    @Test("content view hosts with seeded data without crashing")
    func hostContentView() async throws {
        let appModel = AppModel.preview()
        let runtime = try #require(appModel.runtimes.first)
        await waitForData(runtime)
        host(ContentView().environment(appModel))
    }

    @Test("content view hosts with data arriving after first render")
    func hostContentViewDataArrivesLate() async throws {
        // The app's real launch order: the window is up before the first
        // poll returns, then rows insert into the live List.
        let appModel = AppModel.preview()
        host(ContentView().environment(appModel))
    }
}

import AppKit
import SwiftUI
import Testing
import CockpitAPI
@testable import AtelierCode

/// Hosts the picker in a real window and pumps the run loop so the List
/// actually lays out — catches row-diff crashes previews can't attribute.
@Suite("RemoteFolderPickerSheet hosting smoke")
struct PickerHostingSmokeTest {
    private func pump(seconds: TimeInterval) {
        RunLoop.main.run(until: Date(timeIntervalSinceNow: seconds))
    }

    @Test("sheet hosts, loads default directory without crashing")
    func hostAndNavigate() async throws {
        let sheet = RemoteFolderPickerSheet(client: MockCockpitClient()) { _ in }
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 520, height: 480),
            styleMask: [.titled], backing: .buffered, defer: false
        )
        window.contentView = NSHostingView(rootView: sheet)
        window.orderFront(nil)
        pump(seconds: 0.5)   // default directory load + List render
        window.orderOut(nil)
    }

    @Test("prefilled sheet renders a directory listing without crashing")
    func hostPrefilled() async throws {
        let sheet = RemoteFolderPickerSheet(
            client: MockCockpitClient(),
            initialPath: "/home/dev/Projects/atelier"
        ) { _ in }
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 520, height: 480),
            styleMask: [.titled], backing: .buffered, defer: false
        )
        window.contentView = NSHostingView(rootView: sheet)
        window.orderFront(nil)
        pump(seconds: 0.5)
        window.orderOut(nil)
    }
}

import AppKit
import SwiftUI
import Testing

@testable import ATC

/// Hosts the Settings window in a real window and pumps the run loop,
/// catching crashes previews can't attribute.
@MainActor
@Suite("Settings UI hosting smoke")
struct SettingsHostingSmokeTest {
    private func host(_ view: some View, width: CGFloat = 720, height: CGFloat = 500) {
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: width, height: height),
            styleMask: [.titled], backing: .buffered, defer: false
        )
        window.contentView = NSHostingView(rootView: view)
        window.orderFront(nil)
        pump(seconds: 0.5)
        window.orderOut(nil)
    }

    private func makeSettingsModel(seeded: Bool) -> AppModel {
        let store = makeConnectionsStore()
        if seeded {
            _ = try? store.add(
                name: "Workstation", urlString: "http://workstation.example:7331", token: ""
            )
            _ = try? store.add(name: "Local Dev", urlString: "http://127.0.0.1:7331", token: "secret")
        }
        return AppModel(
            connections: store,
            clientFactory: { _, _ in ScriptableAppServerClient() },
            terminalRecoveryMonitor: .disabled(),
            eventStreamFactory: { _, _ in ScriptedEventStream.inert() }
        )
    }

    @Test("Settings hosts with seeded connections without crashing")
    func hostSeeded() {
        let model = makeSettingsModel(seeded: true)
        #expect(model.connections.connections.count == 2)
        host(SettingsView().environment(model))
    }

    @Test("Settings hosts the no-connections empty state without crashing")
    func hostEmpty() {
        let model = makeSettingsModel(seeded: false)
        #expect(model.connections.connections.isEmpty)
        host(SettingsView().environment(model))
    }
}

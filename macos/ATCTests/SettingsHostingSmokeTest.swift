import AppKit
import SwiftUI
import Testing
import ATCAPI
@testable import ATC

/// Hosts the full Settings window in a real window and pumps the run loop,
/// catching crashes previews can't attribute.
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

    /// A ConnectionsStore backed by a throwaway UserDefaults suite so tests
    /// never touch `.standard`.
    private func makeStore(seeded: Bool) -> ConnectionsStore {
        let suite = "test.settings.\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        defaults.removePersistentDomain(forName: suite)
        let store = ConnectionsStore(defaults: defaults, credentials: InMemoryCredentialStore())
        if seeded {
            _ = try? store.add(name: "Workstation", urlString: "http://workstation.example:7331", token: "")
            _ = try? store.add(name: "Local Dev", urlString: "http://127.0.0.1:7331", token: "secret")
        }
        return store
    }

    @Test("Settings hosts with seeded connections without crashing")
    func hostSeeded() async throws {
        let store = makeStore(seeded: true)
        #expect(store.connections.count == 2)
        host(
            SettingsView()
                .environment(AppModel(connections: store, clientFactory: { _ in MockATCClient() }))
        )
    }

    @Test("Settings hosts with an empty store (empty state) without crashing")
    func hostEmpty() async throws {
        let store = makeStore(seeded: false)
        #expect(store.connections.isEmpty)
        host(
            SettingsView()
                .environment(AppModel(connections: store, clientFactory: { _ in MockATCClient() }))
        )
    }

    @Test("Actions settings hosts with seeded connections without crashing")
    func hostActionsSeeded() async throws {
        let store = makeStore(seeded: true)
        host(
            ActionsSettingsView()
                .environment(AppModel(connections: store, clientFactory: { _ in MockATCClient() }))
        )
    }

    @Test("Actions settings hosts the no-connections empty state without crashing")
    func hostActionsEmpty() async throws {
        let store = makeStore(seeded: false)
        host(
            ActionsSettingsView()
                .environment(AppModel(connections: store, clientFactory: { _ in MockATCClient() }))
        )
    }
}

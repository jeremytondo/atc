import AppKit
import SwiftUI
import Testing
import ATCAPI
@testable import ATC

/// Hosts the picker in a real window and pumps the run loop so the List
/// actually lays out — catches row-diff crashes previews can't attribute.
@Suite("RemoteFolderPickerSheet hosting smoke")
struct PickerHostingSmokeTest {
    @Test("sheet hosts, loads default directory without crashing")
    func hostAndNavigate() async throws {
        let sheet = RemoteFolderPickerSheet(client: MockATCClient()) { _ in }
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
            client: MockATCClient(),
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

    @MainActor
    @Test("indexed toolbar context pill hosts without crashing")
    func hostWorkspaceSwitcher() async throws {
        let model = AppModel.preview()
        await model.refreshAll()
        let state = WindowState.ephemeral()
        let connectionID = try #require(model.runtimes.first?.id)
        let workspace = WorkspaceRef(
            connectionID: connectionID,
            workspaceID: "wsp_parser"
        )
        #expect(state.activateWorkspace(workspace, in: model))
        #expect(state.selectSession(
            SessionRef(connectionID: connectionID, sessionID: "ses_running"),
            in: model
        ))

        let switcher = WorkspaceSwitcher()
            .environment(model)
            .environment(state)
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 560, height: 80),
            styleMask: [.titled], backing: .buffered, defer: false
        )
        window.contentView = NSHostingView(rootView: switcher)
        window.orderFront(nil)
        pump(seconds: 0.25)
        window.orderOut(nil)
    }
}

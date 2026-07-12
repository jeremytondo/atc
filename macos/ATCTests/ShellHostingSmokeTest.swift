import AppKit
import SwiftUI
import Testing
import ATCAPI
@testable import ATC

/// Hosts the full window root (Dashboard cover, Workspace shell with
/// NavigationSplitView + sidebar List + searchable + detail) in a real
/// window and pumps the run loop. This is the hierarchy the app boots
/// into, so it's where launch-time AppKit warnings (reentrant NSTableView
/// delegate operations, invalid Picker selections) surface under a
/// controlled model instead of the developer's live state.
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
            if !runtime.workspaces.workspaces.isEmpty && !runtime.sessions.sessions.isEmpty {
                return
            }
            try? await Task.sleep(for: .milliseconds(20))
        }
    }

    @Test("root view hosts the Dashboard with seeded data without crashing")
    func hostRootOnDashboard() async throws {
        let appModel = AppModel.preview()
        let runtime = try #require(appModel.runtimes.first)
        await waitForData(runtime)
        host(RootView().environment(appModel).environment(WindowState()))
    }

    @Test("root view hosts with data arriving after first render")
    func hostRootDataArrivesLate() async throws {
        // The app's real launch order: the window is up before the first
        // poll returns, then rows insert into the live List.
        let appModel = AppModel.preview()
        host(RootView().environment(appModel).environment(WindowState()))
    }

    @Test("root view hosts the Workspace shell after opening a workspace")
    func hostRootWithOpenWorkspace() async throws {
        let appModel = AppModel.preview()
        let runtime = try #require(appModel.runtimes.first)
        await waitForData(runtime)
        let windowState = WindowState()
        windowState.openWorkspace(
            WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_parser"),
            in: appModel
        )
        host(RootView().environment(appModel).environment(windowState))
        #expect(windowState.route == .workspace)
    }

    @Test("dashboard covers a mounted shell without tearing it down")
    func hostRootDashboardCoveringShell() async throws {
        let appModel = AppModel.preview()
        let runtime = try #require(appModel.runtimes.first)
        await waitForData(runtime)
        let windowState = WindowState()
        windowState.openWorkspace(
            WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_parser"),
            in: appModel
        )
        windowState.showDashboard()
        host(RootView().environment(appModel).environment(windowState))
        // The shell stays mounted (openWorkspace survives the cover).
        #expect(windowState.hasOpenedWorkspaceShell)
        #expect(appModel.openWorkspace != nil)
    }

    @Test("workspace shell hosts an empty workspace with creation actions")
    func hostShellEmptyWorkspace() async throws {
        // prj_notes has zero workspaces; wsp_refactor has sessions — use a
        // workspace whose sessions are filtered to nothing instead: open
        // the archived workspace, which owns no sessions in the fixtures.
        let appModel = AppModel.preview()
        let runtime = try #require(appModel.runtimes.first)
        await waitForData(runtime)
        let windowState = WindowState()
        windowState.openWorkspace(
            WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_archived"),
            in: appModel
        )
        host(RootView().environment(appModel).environment(windowState))
    }
}

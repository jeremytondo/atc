import AppKit
import SwiftUI
import Testing
import ATCAPI
@testable import ATC

/// Hosts the full stable split-view window in a real window and pumps the
/// run loop. This is the hierarchy the app boots into, so launch-time AppKit
/// warnings surface under a controlled model instead of live user state.
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
        host(RootView(configStore: KeyboardConfigStore()).environment(appModel).environment(WindowState.ephemeral()))
    }

    @Test("root view hosts with data arriving after first render")
    func hostRootDataArrivesLate() async throws {
        // The app's real launch order: the window is up before the first
        // poll returns, then rows insert into the live List.
        let appModel = AppModel.preview()
        host(RootView(configStore: KeyboardConfigStore()).environment(appModel).environment(WindowState.ephemeral()))
    }

    @Test("root view hosts Workspace content inside the stable split view")
    func hostRootWithActiveWorkspace() async throws {
        let appModel = AppModel.preview()
        let runtime = try #require(appModel.runtimes.first)
        await waitForData(runtime)
        let windowState = WindowState.ephemeral()
        #expect(windowState.activateWorkspace(
            WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_parser"),
            in: appModel
        ))
        host(RootView(configStore: KeyboardConfigStore()).environment(appModel).environment(windowState))
        #expect(windowState.activeWorkspace?.workspaceID == "wsp_parser")
        #expect(windowState.selectedContent != .dashboard)
    }

    @Test("Dashboard remains a main-content destination with an Active Workspace")
    func hostDashboardWithActiveWorkspace() async throws {
        let appModel = AppModel.preview()
        let runtime = try #require(appModel.runtimes.first)
        await waitForData(runtime)
        let windowState = WindowState.ephemeral()
        #expect(windowState.activateWorkspace(
            WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_parser"),
            in: appModel
        ))
        windowState.showDashboard()
        host(RootView(configStore: KeyboardConfigStore()).environment(appModel).environment(windowState))
        #expect(windowState.selectedContent == .dashboard)
        #expect(windowState.activeWorkspace != nil)
    }

    @Test("removing the Active Workspace's Connection returns to Dashboard")
    func removedConnectionReturnsToDashboard() async throws {
        let appModel = AppModel.preview()
        let runtime = try #require(appModel.runtimes.first)
        await waitForData(runtime)
        let windowState = WindowState.ephemeral()
        #expect(windowState.activateWorkspace(
            WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_parser"),
            in: appModel
        ))

        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 900, height: 600),
            styleMask: [.titled], backing: .buffered, defer: false
        )
        window.contentView = NSHostingView(
            rootView: RootView(configStore: KeyboardConfigStore()).environment(appModel).environment(windowState)
        )
        window.orderFront(nil)
        pump(seconds: 0.5)
        #expect(windowState.activeWorkspace != nil)

        // Window reconciliation observes the AppModel's store projection.
        appModel.removeConnection(id: runtime.id)
        pump(seconds: 0.5)
        window.orderOut(nil)
        #expect(windowState.selectedContent == .dashboard)
        #expect(windowState.activeWorkspace == nil)
    }

    @Test("main content hosts an empty Workspace with creation actions")
    func hostEmptyWorkspace() async throws {
        // prj_notes has zero workspaces; wsp_refactor has sessions — use a
        // workspace whose sessions are filtered to nothing instead: open
        // the archived workspace, which owns no sessions in the fixtures.
        let appModel = AppModel.preview()
        let runtime = try #require(appModel.runtimes.first)
        await waitForData(runtime)
        let windowState = WindowState.ephemeral()
        #expect(windowState.activateWorkspace(
            WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_archived"),
            in: appModel
        ))
        host(RootView(configStore: KeyboardConfigStore()).environment(appModel).environment(windowState))
    }

    @Test("Navigator and Dashboard transitions retain the hosted terminal surface")
    func navigatorTransitionsRetainTerminal() async throws {
        let appModel = AppModel.preview()
        let runtime = try #require(appModel.runtimes.first)
        await waitForData(runtime)
        let windowState = WindowState.ephemeral()
        let workspace = WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_parser")
        let session = SessionRef(connectionID: runtime.id, sessionID: "ses_running")
        #expect(windowState.activateWorkspace(workspace, in: appModel))
        #expect(windowState.selectSession(session, in: appModel))
        let terminal = try #require(appModel.terminals[session])
        #expect(windowState.hasInspectorTarget(in: appModel))

        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 900, height: 600),
            styleMask: [.titled], backing: .buffered, defer: false
        )
        window.contentView = NSHostingView(
            rootView: RootView(configStore: KeyboardConfigStore()).environment(appModel).environment(windowState)
        )
        window.orderFront(nil)

        for navigator in NavigatorID.allCases {
            windowState.selectedNavigator = navigator
            pump(seconds: 0.15)
            #expect(windowState.selectedContent == .session(session))
            #expect(appModel.terminals[session] === terminal)
            #expect(windowState.hasInspectorTarget(in: appModel))
        }

        windowState.showDashboard()
        pump(seconds: 0.15)
        #expect(windowState.activeWorkspace == workspace)
        #expect(appModel.terminals[session] === terminal)
        #expect(!windowState.hasInspectorTarget(in: appModel))
        window.orderOut(nil)
    }

    @Test("Session selection moves first-responder focus between terminal surfaces")
    func sessionSelectionMovesTerminalFocus() async throws {
        let appModel = AppModel.preview()
        let runtime = try #require(appModel.runtimes.first)
        await waitForData(runtime)
        let windowState = WindowState.ephemeral()
        let workspace = WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_parser")
        let firstRef = SessionRef(connectionID: runtime.id, sessionID: "ses_running")
        let secondRef = SessionRef(connectionID: runtime.id, sessionID: "ses_shell")
        #expect(windowState.activateWorkspace(workspace, in: appModel))
        #expect(windowState.selectSession(firstRef, in: appModel))

        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 900, height: 600),
            styleMask: [.titled], backing: .buffered, defer: false
        )
        window.contentView = NSHostingView(
            rootView: RootView(configStore: KeyboardConfigStore())
                .environment(appModel)
                .environment(windowState)
        )
        window.makeKeyAndOrderFront(nil)
        pump(seconds: 0.5)

        let first = try #require(appModel.terminals[firstRef])
        #expect(first.viewState.isFocused)

        #expect(windowState.selectSession(secondRef, in: appModel))
        pump(seconds: 0.5)
        let second = try #require(appModel.terminals[secondRef])
        #expect(!first.viewState.isFocused)
        #expect(second.viewState.isFocused)

        #expect(windowState.selectSession(firstRef, in: appModel))
        pump(seconds: 0.5)
        #expect(first.viewState.isFocused)
        #expect(!second.viewState.isFocused)

        window.makeFirstResponder(nil)
        pump(seconds: 0.1)
        #expect(!first.viewState.isFocused)
        #expect(windowState.selectSession(firstRef, in: appModel))
        pump(seconds: 0.5)
        #expect(first.viewState.isFocused)
        #expect(!second.viewState.isFocused)
        window.orderOut(nil)
    }
}

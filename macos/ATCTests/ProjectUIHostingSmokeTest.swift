import AppKit
import SwiftUI
import Testing
import ATCAPI
@testable import ATC

/// Hosts the Dashboard and the creation sheets in a real window and pumps
/// the run loop — catches List diff crashes and invalid Picker selections
/// previews can't attribute (same rationale as PickerHostingSmokeTest).
@Suite("Dashboard and sheet hosting smoke")
struct ProjectUIHostingSmokeTest {
    private func pump(seconds: TimeInterval) {
        RunLoop.main.run(until: Date(timeIntervalSinceNow: seconds))
    }

    private func host(_ view: some View, width: CGFloat = 460, height: CGFloat = 480) {
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: width, height: height),
            styleMask: [.titled], backing: .buffered, defer: false
        )
        window.contentView = NSHostingView(rootView: view)
        window.orderFront(nil)
        pump(seconds: 0.5)
        window.orderOut(nil)
    }

    /// The runtime's own poll task races a manual `refreshAll` (the stores'
    /// generation guard drops the loser), so wait boundedly for first data
    /// instead of asserting right after one refresh.
    private func waitForData(_ runtime: ConnectionRuntime) async {
        for _ in 0..<100 {
            if !runtime.projects.projects.isEmpty
                && !runtime.workspaces.workspaces.isEmpty
                && !runtime.sessions.sessions.isEmpty { return }
            try? await Task.sleep(for: .milliseconds(20))
        }
    }

    private func dashboard(_ appModel: AppModel) -> some View {
        DashboardView(onOpenWorkspace: { _ in }, onCreateWorkspace: { _ in }, onCreateProject: {})
            .environment(appModel)
    }

    @Test("dashboard renders one populated connection without crashing")
    func hostDashboard() async throws {
        let appModel = AppModel.preview()
        let runtime = try #require(appModel.runtimes.first)
        await waitForData(runtime)
        #expect(!runtime.projects.projects.isEmpty)
        #expect(!runtime.workspaces.workspaces.isEmpty)
        host(dashboard(appModel), width: 700, height: 560)
    }

    @Test("dashboard renders two connections without crashing")
    func hostDashboardTwoConnections() async throws {
        let appModel = AppModel.preview(connections: [
            (name: "Workstation", client: MockATCClient()),
            (name: "Laptop", client: MockATCClient()),
        ])
        for runtime in appModel.runtimes { await waitForData(runtime) }
        host(dashboard(appModel), width: 700, height: 560)
    }

    @Test("dashboard renders the no-connections empty state without crashing")
    func hostDashboardNoConnections() async throws {
        let suite = "ProjectUIHostingSmokeTest.\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        defaults.removePersistentDomain(forName: suite)
        let appModel = AppModel(
            connections: ConnectionsStore(defaults: defaults, credentials: InMemoryCredentialStore()),
            clientFactory: { _ in MockATCClient() }
        )
        #expect(appModel.runtimes.isEmpty)
        host(dashboard(appModel), width: 700, height: 560)
    }

    @Test("all Navigator sidebar modes host without crashing")
    func hostNavigatorSidebarModes() async throws {
        let appModel = AppModel.preview()
        let runtime = try #require(appModel.runtimes.first)
        await waitForData(runtime)
        let state = WindowState()
        let sidebar = NavigatorSidebar()
            .environment(appModel)
            .environment(state)

        host(sidebar, width: 280, height: 560)
        let workspace = WorkspaceRef(
            connectionID: runtime.id,
            workspaceID: "wsp_parser"
        )
        #expect(state.activateWorkspace(workspace, in: appModel))
        state.selectedNavigator = .workspace
        host(sidebar, width: 280, height: 560)
        state.selectedNavigator = .file
        host(sidebar, width: 280, height: 560)
    }

    @Test("create-project sheet hosts without crashing")
    func hostCreateProject() async throws {
        host(
            CreateProjectSheet()
                .environment(AppModel.preview())
        )
    }

    @Test("create-workspace sheet hosts in all three contexts without crashing")
    func hostCreateWorkspace() async throws {
        let appModel = AppModel.preview()
        let runtime = try #require(appModel.runtimes.first)
        await waitForData(runtime)
        let ref = ProjectRef(connectionID: runtime.id, projectID: "prj_atelier")
        host(
            CreateWorkspaceSheet(context: CreateWorkspaceContext(mode: .fixed(ref)))
                .environment(appModel)
        )
        host(
            CreateWorkspaceSheet(context: CreateWorkspaceContext(mode: .preselected(ref)))
                .environment(appModel)
        )
        host(
            CreateWorkspaceSheet(context: CreateWorkspaceContext(mode: .free))
                .environment(appModel)
        )
    }

    @Test("new-session and new-terminal sheets host without crashing")
    func hostStartSessionSheets() async throws {
        let appModel = AppModel.preview()
        let runtime = try #require(appModel.runtimes.first)
        await waitForData(runtime)
        // Actions arrive with the poll cycle; wait so the picker has data.
        for _ in 0..<100 where runtime.actions.actions.isEmpty {
            try? await Task.sleep(for: .milliseconds(20))
        }
        let ref = WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_parser")
        host(StartWorkspaceSessionSheet(kind: .agentSession, workspaceRef: ref).environment(appModel))
        host(StartWorkspaceSessionSheet(kind: .terminal, workspaceRef: ref).environment(appModel))
    }
}

import Foundation
import Testing
import ATCAPI
@testable import ATC

/// Window routing, command availability, selection memory, and the
/// delete-confirmation copy — the state layer behind the Dashboard and
/// Workspace shell flows.
@MainActor
@Suite("Workspace flows")
struct WorkspaceFlowTests {
    private func makeModel(
        client: @escaping @autoclosure () -> any ATCClient = MockATCClient()
    ) -> AppModel {
        let suite = "WorkspaceFlowTests.\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        defaults.removePersistentDomain(forName: suite)
        let store = ConnectionsStore(defaults: defaults, credentials: InMemoryCredentialStore())
        return AppModel(
            connections: store,
            clientFactory: { _ in client() },
            terminalRecoveryMonitor: .disabled()
        )
    }

    /// A connected runtime with fixture data loaded and polling stopped.
    private func makeOpenedModel() async throws -> (AppModel, WindowState, ConnectionRuntime) {
        let model = makeModel()
        let record = try model.addConnection(name: "A", urlString: "http://a:1", token: "")
        let runtime = model.runtime(id: record.id)!
        runtime.stopPolling()
        await runtime.refresh()
        let windowState = WindowState()
        windowState.openWorkspace(
            WorkspaceRef(connectionID: record.id, workspaceID: "wsp_parser"),
            in: model
        )
        return (model, windowState, runtime)
    }

    // MARK: - Routing

    @Test("opening a workspace routes to the shell and mounts it for good")
    func openRoutes() async throws {
        let (model, windowState, _) = try await makeOpenedModel()
        #expect(windowState.route == .workspace)
        #expect(windowState.hasOpenedWorkspaceShell)
        #expect(model.openWorkspace?.workspaceID == "wsp_parser")

        // Back to the Dashboard covers the shell but keeps it open.
        windowState.showDashboard()
        #expect(windowState.route == .dashboard)
        #expect(windowState.hasOpenedWorkspaceShell)
        #expect(model.openWorkspace != nil)
    }

    @Test("a vanished open workspace routes back to the dashboard")
    func vanishedWorkspaceRoutesBack() async throws {
        let (model, windowState, runtime) = try await makeOpenedModel()
        #expect(model.openWorkspaceExists)

        // Simulate a remote delete: the store no longer lists it.
        model.openWorkspace = WorkspaceRef(
            connectionID: runtime.id, workspaceID: "wsp_deleted_elsewhere"
        )
        #expect(!model.openWorkspaceExists)
        windowState.handleOpenWorkspaceGone(in: model)
        #expect(windowState.route == .dashboard)
        #expect(model.openWorkspace == nil)
    }

    @Test("removing the connection makes the open workspace not exist")
    func removedConnectionRoutesBack() async throws {
        let (model, _, runtime) = try await makeOpenedModel()
        model.removeConnection(id: runtime.id)
        // teardown already cleared openWorkspace; a cleared ref reads as gone.
        #expect(model.openWorkspace == nil)
        #expect(!model.openWorkspaceExists)
    }

    // MARK: - Command availability

    @Test("session and terminal creation need the shell route")
    func creationNeedsShellRoute() async throws {
        let (model, windowState, _) = try await makeOpenedModel()
        #expect(windowState.canStartSession(in: model))
        windowState.showDashboard()
        #expect(!windowState.canStartSession(in: model))
    }

    @Test("session creation is disabled in an archived workspace")
    func creationDisabledWhenArchived() async throws {
        let (model, windowState, runtime) = try await makeOpenedModel()
        windowState.openWorkspace(
            WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_archived"),
            in: model
        )
        #expect(!windowState.canStartSession(in: model))
    }

    @Test("session creation is disabled on an unreachable connection")
    func creationDisabledWhenUnreachable() async throws {
        let client = StatefulWorkspacesClient()
        let model = makeModel(client: client)
        let record = try model.addConnection(name: "A", urlString: "http://a:1", token: "")
        let runtime = model.runtime(id: record.id)!
        runtime.stopPolling()
        await runtime.refresh()
        let windowState = WindowState()
        windowState.openWorkspace(
            WorkspaceRef(connectionID: record.id, workspaceID: "wsp_a"),
            in: model
        )
        #expect(windowState.canStartSession(in: model))

        // Flip the whole connection red: cached data stays, creation stops.
        client.failSessions = true
        await runtime.refresh()
        #expect(runtime.reachability == .unreachable)
        #expect(!windowState.canStartSession(in: model))
    }

    @Test("new-workspace preselects the open workspace's project, else runs context-free")
    func createWorkspaceContexts() async throws {
        let (model, windowState, runtime) = try await makeOpenedModel()
        windowState.presentCreateWorkspace(in: model)
        #expect(windowState.createWorkspaceContext?.mode == .preselected(
            ProjectRef(connectionID: runtime.id, projectID: "prj_atelier")
        ))

        windowState.createWorkspaceContext = nil
        windowState.showDashboard()
        windowState.presentCreateWorkspace(in: model)
        #expect(windowState.createWorkspaceContext?.mode == .free)
    }

    // MARK: - Selection memory

    @Test("selection memory remembers, restores, and forgets per workspace")
    func selectionMemory() {
        let suite = "WorkspaceFlowTests.memory.\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        defaults.removePersistentDomain(forName: suite)
        let memory = WorkspaceSelectionMemory(defaults: defaults)

        #expect(memory.sessionID(for: "wsp_a") == nil)
        memory.remember(sessionID: "ses_1", for: "wsp_a")
        memory.remember(sessionID: "ses_2", for: "wsp_b")
        #expect(memory.sessionID(for: "wsp_a") == "ses_1")
        #expect(memory.sessionID(for: "wsp_b") == "ses_2")

        memory.remember(sessionID: "ses_9", for: "wsp_a")
        #expect(memory.sessionID(for: "wsp_a") == "ses_9")

        memory.forget(workspaceID: "wsp_a")
        #expect(memory.sessionID(for: "wsp_a") == nil)
        #expect(memory.sessionID(for: "wsp_b") == "ses_2")
    }

    // MARK: - Delete confirmations

    @Test("every delete confirmation names its target and ends with the ADR sentence")
    func deleteConfirmationCopy() {
        let session = DeleteConfirmation.sessionMessage(displayName: "Fix parser")
        #expect(session.contains("“Fix parser”"))
        #expect(session.hasSuffix(DeleteConfirmation.filesUntouched))

        let quiet = DeleteConfirmation.workspaceMessage(name: "Spike", sessionCount: 1, activeCount: 0)
        #expect(quiet.contains("“Spike” and its 1 session?"))
        #expect(!quiet.contains("will be stopped"))
        #expect(quiet.hasSuffix(DeleteConfirmation.filesUntouched))

        let busy = DeleteConfirmation.workspaceMessage(name: "Spike", sessionCount: 3, activeCount: 2)
        #expect(busy.contains("its 3 sessions?"))
        #expect(busy.contains("2 running sessions will be stopped."))
        #expect(busy.hasSuffix(DeleteConfirmation.filesUntouched))

        let project = DeleteConfirmation.projectMessage(name: "Atelier")
        #expect(project.contains("“Atelier”"))
        #expect(project.hasSuffix(DeleteConfirmation.filesUntouched))
    }

    // MARK: - Session store wrappers

    @Test("session delete removes the row; unarchive clears archivedAt")
    func sessionWrappers() async throws {
        // Stateful client: the store's follow-up refreshes must not
        // resurrect the deleted/archived fixtures mid-test.
        let model = makeModel(client: StatefulWorkspacesClient())
        let record = try model.addConnection(name: "A", urlString: "http://a:1", token: "")
        let runtime = model.runtime(id: record.id)!
        runtime.stopPolling()
        await runtime.refresh()

        let store = runtime.sessions
        #expect(store.session(id: "ses_archived")?.isArchived == true)
        try await store.unarchive(id: "ses_archived")
        #expect(store.session(id: "ses_archived")?.isArchived == false)

        try await store.delete(id: "ses_done")
        #expect(store.session(id: "ses_done") == nil)
    }
}

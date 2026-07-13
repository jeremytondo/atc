import Foundation
import Testing
import ATCAPI
@testable import ATC

@MainActor
@Suite("Workspace flows")
struct WorkspaceFlowTests {
    private func makeModel(
        client: @escaping @autoclosure () -> any ATCClient = MockATCClient()
    ) -> AppModel {
        let suite = "WorkspaceFlowTests.model.\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        defaults.removePersistentDomain(forName: suite)
        return AppModel(
            connections: ConnectionsStore(
                defaults: defaults,
                credentials: InMemoryCredentialStore()
            ),
            clientFactory: { _ in client() },
            terminalRecoveryMonitor: .disabled()
        )
    }

    private func makeLoadedModel(
        client: @escaping @autoclosure () -> any ATCClient = MockATCClient()
    ) async throws -> (AppModel, ConnectionRuntime) {
        let model = makeModel(client: client())
        let record = try model.addConnection(name: "A", urlString: "http://a:1", token: "")
        let runtime = try #require(model.runtime(id: record.id))
        runtime.stopPolling()
        await runtime.refresh()
        return (model, runtime)
    }

    private func memory() -> (WorkspaceSelectionMemory, UserDefaults) {
        let suite = "WorkspaceFlowTests.memory.\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        defaults.removePersistentDomain(forName: suite)
        return (WorkspaceSelectionMemory(defaults: defaults), defaults)
    }

    @Test("launch defaults select Projects and Dashboard with no Active Workspace")
    func launchDefaults() {
        let state = WindowState()
        #expect(state.selectedNavigator == .projects)
        #expect(state.selectedContent == .dashboard)
        #expect(state.activeWorkspace == nil)
        #expect(!state.isInspectorPresented)
        #expect(state.columnVisibility == .all)
    }

    @Test("Navigator changes preserve main content and inspector")
    func navigatorPreservesContent() async throws {
        let (model, runtime) = try await makeLoadedModel()
        let state = WindowState()
        let workspace = WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_parser")
        #expect(state.activateWorkspace(workspace, in: model))
        let session = SessionRef(connectionID: runtime.id, sessionID: "ses_running")
        #expect(state.selectSession(session, in: model))
        state.isInspectorPresented = true

        state.selectedNavigator = .workspace
        #expect(state.selectedContent == .session(session))
        #expect(state.isInspectorPresented)
        state.selectedNavigator = .file
        #expect(state.selectedContent == .session(session))
        #expect(state.isInspectorPresented)
    }

    @Test("Dashboard preserves Active Workspace, Navigator, and command availability")
    func dashboardPreservesWorkspaceContext() async throws {
        let (model, runtime) = try await makeLoadedModel()
        let state = WindowState()
        let workspace = WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_parser")
        #expect(state.activateWorkspace(workspace, in: model))
        state.selectedNavigator = .file
        state.isInspectorPresented = true

        state.showDashboard()
        #expect(state.selectedContent == .dashboard)
        #expect(state.activeWorkspace == workspace)
        #expect(state.selectedNavigator == .file)
        #expect(!state.isInspectorPresented)
        #expect(state.canStartSession(in: model))
    }

    @Test("activation preserves Navigator and restores valid remembered content")
    func activationRestoresSelection() async throws {
        let (model, runtime) = try await makeLoadedModel()
        let (selectionMemory, _) = memory()
        let state = WindowState(selectionMemory: selectionMemory)
        state.selectedNavigator = .file
        let workspace = WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_parser")
        selectionMemory.remember(sessionID: "ses_running", for: workspace)

        #expect(state.activateWorkspace(workspace, in: model))
        #expect(state.activeWorkspace == workspace)
        #expect(state.selectedNavigator == .file)
        #expect(state.selectedContent == .session(
            SessionRef(connectionID: runtime.id, sessionID: "ses_running")
        ))
    }

    @Test("same Workspace activation is idempotent")
    func activationIsIdempotent() async throws {
        let (model, runtime) = try await makeLoadedModel()
        let state = WindowState()
        let workspace = WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_parser")
        let selected = SessionRef(connectionID: runtime.id, sessionID: "ses_running")
        #expect(state.activateWorkspace(workspace, in: model))
        #expect(state.selectSession(selected, in: model))
        state.isInspectorPresented = true

        #expect(state.activateWorkspace(workspace, in: model))
        #expect(state.selectedContent == .session(selected))
        #expect(state.isInspectorPresented)
    }

    @Test("cross-Workspace and cross-Connection session selections are rejected")
    func invalidSelectionsRejected() async throws {
        let (model, runtime) = try await makeLoadedModel()
        let other = try model.addConnection(name: "B", urlString: "http://b:1", token: "")
        model.runtime(id: other.id)?.stopPolling()
        await model.runtime(id: other.id)?.refresh()
        let state = WindowState()
        let workspace = WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_parser")
        #expect(state.activateWorkspace(workspace, in: model))

        #expect(!state.selectSession(
            SessionRef(connectionID: runtime.id, sessionID: "ses_ghost"), in: model
        ))
        #expect(!state.selectSession(
            SessionRef(connectionID: other.id, sessionID: "ses_running"), in: model
        ))
        #expect(state.selectedContent == .workspace(workspace))
    }

    @Test("different Workspace activation closes inspector and never keeps stale content")
    func switchingWorkspaceClearsStaleContent() async throws {
        let (model, runtime) = try await makeLoadedModel()
        let state = WindowState()
        let first = WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_parser")
        let second = WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_refactor")
        #expect(state.activateWorkspace(first, in: model))
        #expect(state.selectSession(
            SessionRef(connectionID: runtime.id, sessionID: "ses_running"), in: model
        ))
        state.isInspectorPresented = true

        #expect(state.activateWorkspace(second, in: model))
        #expect(state.activeWorkspace == second)
        #expect(state.selectedContent == .workspace(second))
        #expect(!state.isInspectorPresented)
    }

    @Test("an open inspector follows a new Session in the same Workspace")
    func inspectorFollowsSelection() async throws {
        let (model, runtime) = try await makeLoadedModel()
        let state = WindowState()
        #expect(state.activateWorkspace(
            WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_parser"), in: model
        ))
        #expect(state.selectSession(
            SessionRef(connectionID: runtime.id, sessionID: "ses_running"), in: model
        ))
        state.isInspectorPresented = true
        let next = SessionRef(connectionID: runtime.id, sessionID: "ses_shell")
        #expect(state.selectSession(next, in: model))
        #expect(state.selectedContent == .session(next))
        #expect(state.isInspectorPresented)
    }

    @Test("clearing or archiving selected content clears remembered restoration")
    func staleSelectionMemoryIsCleared() async throws {
        let (model, runtime) = try await makeLoadedModel()
        let (selectionMemory, _) = memory()
        let state = WindowState(selectionMemory: selectionMemory)
        let parser = WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_parser")
        #expect(state.activateWorkspace(parser, in: model))
        #expect(state.selectSession(
            SessionRef(connectionID: runtime.id, sessionID: "ses_running"), in: model
        ))
        #expect(selectionMemory.sessionID(for: parser) == "ses_running")
        state.showWorkspaceEmpty()
        #expect(selectionMemory.sessionID(for: parser) == nil)

        let refactor = WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_refactor")
        #expect(state.activateWorkspace(refactor, in: model))
        let ended = SessionRef(connectionID: runtime.id, sessionID: "ses_done")
        #expect(state.selectSession(ended, in: model))
        try await runtime.sessions.archive(id: ended.sessionID)
        state.reconcile(in: model)
        #expect(state.selectedContent == .session(ended))
        #expect(selectionMemory.sessionID(for: refactor) == nil)
    }

    @Test("selecting archived content never writes restoration memory")
    func archivedSelectionIsNotRemembered() async throws {
        let (model, runtime) = try await makeLoadedModel()
        let (selectionMemory, _) = memory()
        let state = WindowState(selectionMemory: selectionMemory)
        let workspace = WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_parser")
        let archived = SessionRef(connectionID: runtime.id, sessionID: "ses_archived")

        #expect(state.activateWorkspace(workspace, in: model))
        #expect(state.selectSession(archived, in: model))
        #expect(state.selectedContent == .session(archived))
        #expect(selectionMemory.sessionID(for: workspace) == nil)
    }

    @Test("a failed first Workspace load after rebuild preserves window context")
    func failedWorkspaceLoadAfterRebuildPreservesContext() async throws {
        let client = StatefulWorkspacesClient()
        let (model, runtime) = try await makeLoadedModel(client: client)
        let (selectionMemory, _) = memory()
        let state = WindowState(selectionMemory: selectionMemory)
        let workspace = WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_a")
        #expect(state.activateWorkspace(workspace, in: model))

        client.failWorkspaces = true
        try model.updateConnection(
            id: runtime.id, name: "A", urlString: "http://a:2", token: ""
        )
        let rebuilt = try #require(model.runtime(id: runtime.id))
        rebuilt.stopPolling()
        await rebuilt.refresh()
        state.reconcile(in: model)

        #expect(rebuilt.workspaces.hasLoadedOnce)
        #expect(rebuilt.workspaces.lastError != nil)
        #expect(state.activeWorkspace == workspace)

        client.failWorkspaces = false
        await rebuilt.refresh()
        state.reconcile(in: model)
        #expect(state.activeWorkspace == workspace)
    }

    @Test("failed first Sessions load preserves memory and restores after recovery")
    func failedSessionsLoadPreservesPendingRestore() async throws {
        let client = StatefulWorkspacesClient()
        client.failSessions = true
        let model = makeModel(client: client)
        let record = try model.addConnection(name: "A", urlString: "http://a:1", token: "")
        let runtime = try #require(model.runtime(id: record.id))
        runtime.stopPolling()
        await runtime.refresh()

        let (selectionMemory, _) = memory()
        let state = WindowState(selectionMemory: selectionMemory)
        let workspace = WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_a")
        selectionMemory.remember(sessionID: "ses_running", for: workspace)
        #expect(state.activateWorkspace(workspace, in: model))
        #expect(state.selectedContent == .workspace(workspace))
        #expect(selectionMemory.sessionID(for: workspace) == "ses_running")

        client.failSessions = false
        await runtime.refresh()
        state.reconcile(in: model)
        #expect(state.selectedContent == .session(
            SessionRef(connectionID: runtime.id, sessionID: "ses_running")
        ))
    }

    @Test("unresolved and disconnected stores preserve the Active Workspace")
    func unresolvedAndDisconnectedPreserveWorkspace() async throws {
        let client = StatefulWorkspacesClient()
        let (model, runtime) = try await makeLoadedModel(client: client)
        let state = WindowState()
        let workspace = WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_a")
        #expect(state.activateWorkspace(workspace, in: model))

        try model.updateConnection(
            id: runtime.id, name: "A", urlString: "http://a:2", token: ""
        )
        state.reconcile(in: model)
        #expect(state.activeWorkspace == workspace)

        let rebuilt = try #require(model.runtime(id: runtime.id))
        rebuilt.stopPolling()
        await rebuilt.refresh()
        state.reconcile(in: model)
        #expect(state.activeWorkspace == workspace)
    }

    @Test("removed Connection clears window references and returns to Dashboard")
    func removedConnectionClearsWindow() async throws {
        let (model, runtime) = try await makeLoadedModel()
        let state = WindowState()
        let workspace = WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_parser")
        #expect(state.activateWorkspace(workspace, in: model))
        state.selectedNavigator = .workspace
        state.isInspectorPresented = true

        model.removeConnection(id: runtime.id)
        state.reconcile(in: model)
        #expect(state.activeWorkspace == nil)
        #expect(state.selectedNavigator == .projects)
        #expect(state.selectedContent == .dashboard)
        #expect(!state.isInspectorPresented)
    }

    @Test("confirmed Workspace removal clears window references")
    func removedWorkspaceClearsWindow() async throws {
        let client = StatefulWorkspacesClient()
        let (model, runtime) = try await makeLoadedModel(client: client)
        let state = WindowState()
        let workspace = WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_a")
        #expect(state.activateWorkspace(workspace, in: model))
        try await runtime.workspaces.delete(id: workspace.workspaceID)
        state.reconcile(in: model)
        #expect(state.activeWorkspace == nil)
        #expect(state.selectedNavigator == .projects)
        #expect(state.selectedContent == .dashboard)
    }

    @Test("archiving an Active Workspace preserves it and disables creation")
    func archivedWorkspaceRemainsActive() async throws {
        let client = StatefulWorkspacesClient()
        let (model, runtime) = try await makeLoadedModel(client: client)
        let state = WindowState()
        let workspace = WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_a")
        #expect(state.activateWorkspace(workspace, in: model))
        try await runtime.workspaces.archive(id: workspace.workspaceID)
        state.reconcile(in: model)
        #expect(state.activeWorkspace == workspace)
        #expect(!state.canStartSession(in: model))
    }

    @Test("creation availability depends on Active Workspace, archive, and reachability")
    func creationAvailability() async throws {
        let client = StatefulWorkspacesClient()
        let (model, runtime) = try await makeLoadedModel(client: client)
        let state = WindowState()
        #expect(state.activateWorkspace(
            WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_a"), in: model
        ))
        state.showDashboard()
        #expect(state.canStartSession(in: model))

        client.failSessions = true
        await runtime.refresh()
        #expect(!state.canStartSession(in: model))

        client.failSessions = false
        await runtime.refresh()
        let (archiveModel, archiveRuntime) = try await makeLoadedModel()
        let archiveState = WindowState()
        #expect(archiveState.activateWorkspace(
            WorkspaceRef(connectionID: archiveRuntime.id, workspaceID: "wsp_archived"),
            in: archiveModel
        ))
        #expect(!archiveState.canStartSession(in: archiveModel))
    }

    @Test("New Workspace uses Active Project even while Dashboard is visible")
    func createWorkspaceContextUsesActiveWorkspace() async throws {
        let (model, runtime) = try await makeLoadedModel()
        let state = WindowState()
        #expect(state.activateWorkspace(
            WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_parser"), in: model
        ))
        state.showDashboard()
        state.presentCreateWorkspace(in: model)
        #expect(state.createWorkspaceContext?.mode == .preselected(
            ProjectRef(connectionID: runtime.id, projectID: "prj_atelier")
        ))
    }

    @Test("captured creation targets revalidate reachability and archive state")
    func mutationTargetsRevalidate() async throws {
        let client = StatefulWorkspacesClient()
        let (model, runtime) = try await makeLoadedModel(client: client)
        let project = ProjectRef(connectionID: runtime.id, projectID: "prj_one")
        let workspace = WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_a")

        #expect(model.canMutate(connectionID: runtime.id))
        #expect(model.canCreateWorkspace(in: project))
        #expect(model.canStartSession(in: workspace))

        client.failSessions = true
        await runtime.refresh()
        #expect(!model.canMutate(connectionID: runtime.id))
        #expect(!model.canCreateWorkspace(in: project))
        #expect(!model.canStartSession(in: workspace))

        client.failSessions = false
        await runtime.refresh()
        #expect(model.canCreateWorkspace(in: project))
        #expect(model.canStartSession(in: workspace))

        try await runtime.workspaces.archive(id: workspace.workspaceID)
        #expect(!model.canStartSession(in: workspace))
    }

    @Test("selection memory is Connection-qualified and discards the obsolete map")
    func selectionMemoryIsComposite() {
        let (selectionMemory, defaults) = memory()
        defaults.set(["same": "old"], forKey: "workspaceSelections")
        let memoryAfterOldData = WorkspaceSelectionMemory(defaults: defaults)
        let a = WorkspaceRef(connectionID: UUID(), workspaceID: "same")
        let b = WorkspaceRef(connectionID: UUID(), workspaceID: "same")

        memoryAfterOldData.remember(sessionID: "ses_a", for: a)
        memoryAfterOldData.remember(sessionID: "ses_b", for: b)
        #expect(memoryAfterOldData.sessionID(for: a) == "ses_a")
        #expect(memoryAfterOldData.sessionID(for: b) == "ses_b")
        #expect(defaults.object(forKey: "workspaceSelections") == nil)

        selectionMemory.forget(a)
        #expect(selectionMemory.sessionID(for: a) == nil)
        #expect(selectionMemory.sessionID(for: b) == "ses_b")
    }

    @Test("restoration rejects missing, moved, and archived sessions")
    func selectionRestoreFallbacks() {
        let (memory, _) = memory()
        let ref = WorkspaceRef(connectionID: UUID(), workspaceID: "wsp_a")
        var session = Session(
            id: "ses_1", environment: "host", workingDir: "/home/dev",
            status: .running, attachable: true,
            createdAt: .now, updatedAt: .now,
            workspace: SessionWorkspace(id: "wsp_a", name: "A")
        )
        memory.remember(sessionID: session.id, for: ref)
        for status in [SessionStatus.running, .terminated, .failed] {
            session.status = status
            #expect(memory.restoredSelection(for: ref, in: [session]) != nil)
        }
        #expect(memory.restoredSelection(for: ref, in: []) == nil)

        session.workspace = SessionWorkspace(id: "wsp_b", name: "B")
        #expect(memory.restoredSelection(for: ref, in: [session]) == nil)
        session.workspace = SessionWorkspace(id: "wsp_a", name: "A")
        session.archivedAt = .now
        #expect(memory.restoredSelection(for: ref, in: [session]) == nil)
    }

    @Test("every delete confirmation names its target and preserves file copy")
    func deleteConfirmationCopy() {
        let session = DeleteConfirmation.sessionMessage(displayName: "Fix parser")
        #expect(session.contains("“Fix parser”"))
        #expect(session.hasSuffix(DeleteConfirmation.filesUntouched))
        let workspace = DeleteConfirmation.workspaceMessage(
            name: "Spike", sessionCount: 3, activeCount: 2
        )
        #expect(workspace.contains("2 running sessions will be stopped."))
        #expect(workspace.hasSuffix(DeleteConfirmation.filesUntouched))
        #expect(DeleteConfirmation.projectMessage(name: "Atelier")
            .hasSuffix(DeleteConfirmation.filesUntouched))
    }

    @Test("session delete and unarchive wrappers update the store")
    func sessionWrappers() async throws {
        let model = makeModel(client: StatefulWorkspacesClient())
        let record = try model.addConnection(name: "A", urlString: "http://a:1", token: "")
        let runtime = try #require(model.runtime(id: record.id))
        runtime.stopPolling()
        await runtime.refresh()
        #expect(runtime.sessions.session(id: "ses_archived")?.isArchived == true)
        try await runtime.sessions.unarchive(id: "ses_archived")
        #expect(runtime.sessions.session(id: "ses_archived")?.isArchived == false)
        try await runtime.sessions.delete(id: "ses_done")
        #expect(runtime.sessions.session(id: "ses_done") == nil)
    }
}

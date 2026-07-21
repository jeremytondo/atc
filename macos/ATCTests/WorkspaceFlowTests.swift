import Foundation
import SwiftUI
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
        let state = WindowState.ephemeral()
        #expect(state.selectedNavigator == .projects)
        #expect(state.selectedContent == .dashboard)
        #expect(state.activeWorkspace == nil)
        #expect(!state.isInspectorPresented)
        #expect(state.columnVisibility == .all)
        #expect(state.isProjectsSectionExpanded)
        #expect(state.isSessionsSectionExpanded)
        #expect(state.isTerminalsSectionExpanded)
    }

    @Test("Navigator changes preserve main content and inspector")
    func navigatorPreservesContent() async throws {
        let (model, runtime) = try await makeLoadedModel()
        let state = WindowState.ephemeral()
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

    @Test("selecting and reselecting a Session requests terminal focus")
    func sessionSelectionRequestsTerminalFocus() async throws {
        let (model, runtime) = try await makeLoadedModel()
        let state = WindowState.ephemeral()
        let workspace = WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_parser")
        let session = SessionRef(connectionID: runtime.id, sessionID: "ses_running")
        #expect(state.activateWorkspace(workspace, in: model))

        #expect(state.terminalFocusRequest == 0)
        #expect(state.selectSession(session, in: model))
        let firstRequest = state.terminalFocusRequest
        #expect(firstRequest > 0)

        #expect(state.selectSession(session, in: model))
        #expect(state.terminalFocusRequest > firstRequest)
    }

    @Test("dismissed transient UI can re-request focus for the selected Session")
    func selectedSessionCanRequestFocusAgain() async throws {
        let (model, runtime) = try await makeLoadedModel()
        let state = WindowState.ephemeral()
        #expect(state.activateWorkspace(
            WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_parser"),
            in: model
        ))
        #expect(state.selectSession(
            SessionRef(connectionID: runtime.id, sessionID: "ses_running"),
            in: model
        ))
        let selectionRequest = state.terminalFocusRequest

        state.requestTerminalFocus()

        #expect(state.terminalFocusRequest > selectionRequest)
    }

    @Test("Dashboard preserves Active Workspace, Navigator, and command availability")
    func dashboardPreservesWorkspaceContext() async throws {
        let (model, runtime) = try await makeLoadedModel()
        let state = WindowState.ephemeral()
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
        #expect(state.expandedProjects.contains(ProjectRef(
            connectionID: runtime.id,
            projectID: "prj_atelier"
        )))
        #expect(state.selectedContent == .session(
            SessionRef(connectionID: runtime.id, sessionID: "ses_running")
        ))
    }

    @Test("same Workspace activation is idempotent")
    func activationIsIdempotent() async throws {
        let (model, runtime) = try await makeLoadedModel()
        let state = WindowState.ephemeral()
        let workspace = WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_parser")
        let selected = SessionRef(connectionID: runtime.id, sessionID: "ses_running")
        #expect(state.activateWorkspace(workspace, in: model))
        #expect(state.selectSession(selected, in: model))
        state.isInspectorPresented = true

        #expect(state.activateWorkspace(workspace, in: model))
        #expect(state.selectedContent == .session(selected))
        #expect(state.isInspectorPresented)
    }

    @Test("reselecting the Active Workspace returns from Dashboard")
    func activeWorkspaceReturnsFromDashboard() async throws {
        let (model, runtime) = try await makeLoadedModel()
        let state = WindowState.ephemeral()
        let workspace = WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_parser")
        let selected = SessionRef(connectionID: runtime.id, sessionID: "ses_running")
        #expect(state.activateWorkspace(workspace, in: model))
        #expect(state.selectSession(selected, in: model))

        state.showDashboard()
        #expect(state.activateWorkspace(workspace, in: model))

        #expect(state.activeWorkspace == workspace)
        #expect(state.selectedContent == .session(selected))
    }

    @Test("cross-Workspace and cross-Connection session selections are rejected")
    func invalidSelectionsRejected() async throws {
        let (model, runtime) = try await makeLoadedModel()
        let other = try model.addConnection(name: "B", urlString: "http://b:1", token: "")
        model.runtime(id: other.id)?.stopPolling()
        await model.runtime(id: other.id)?.refresh()
        let state = WindowState.ephemeral()
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
        let state = WindowState.ephemeral()
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
        let state = WindowState.ephemeral()
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

    @Test("clearing selected content clears remembered restoration")
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

    }

    @Test("explicit Disconnect is not undone by reconcile")
    func disconnectSurvivesReconcile() async throws {
        let (model, runtime) = try await makeLoadedModel()
        let (selectionMemory, _) = memory()
        let state = WindowState(selectionMemory: selectionMemory)
        let workspace = WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_parser")
        let selected = SessionRef(connectionID: runtime.id, sessionID: "ses_running")
        #expect(state.activateWorkspace(workspace, in: model))
        #expect(state.selectSession(selected, in: model))
        #expect(model.terminals[selected] != nil)

        model.disconnectTerminal(ref: selected)
        state.reconcile(in: model)
        #expect(model.terminals[selected] == nil)

        // An explicit attach (Connect button, re-selection) reconnects and
        // reconcile keeps it connected afterwards.
        let session = try #require(model.session(for: selected))
        model.attachIfNeeded(to: session, connectionID: selected.connectionID)
        state.reconcile(in: model)
        #expect(model.terminals[selected] != nil)
    }

    @Test("Live to Ended refresh tears down interaction and preserves selection")
    func liveToEndedRefresh() async throws {
        let client = StatefulWorkspacesClient()
        let (model, runtime) = try await makeLoadedModel(client: client)
        let state = WindowState.ephemeral()
        let workspace = WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_a")
        let selected = SessionRef(connectionID: runtime.id, sessionID: "ses_running")
        #expect(state.activateWorkspace(workspace, in: model))
        #expect(state.selectSession(selected, in: model))
        #expect(model.terminals[selected] != nil)

        client.endSession(id: selected.sessionID)
        await runtime.sessions.refresh()
        model.reconcileTerminalLifecycle()
        state.reconcile(in: model)

        #expect(model.session(for: selected)?.status == .ended)
        #expect(model.terminals[selected] == nil)
        #expect(state.selectedContent == .session(selected))
    }

    @Test("failed session refresh preserves last-known Live state and interaction")
    func failedRefreshPreservesLiveState() async throws {
        let client = StatefulWorkspacesClient()
        let (model, runtime) = try await makeLoadedModel(client: client)
        let state = WindowState.ephemeral()
        let workspace = WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_a")
        let selected = SessionRef(connectionID: runtime.id, sessionID: "ses_running")
        #expect(state.activateWorkspace(workspace, in: model))
        #expect(state.selectSession(selected, in: model))

        client.failSessions = true
        await runtime.sessions.refresh()
        model.reconcileTerminalLifecycle()
        state.reconcile(in: model)

        #expect(runtime.sessions.lastError != nil)
        #expect(model.session(for: selected)?.status == .live)
        #expect(model.terminals[selected] != nil)
        #expect(state.selectedContent == .session(selected))
    }

    @Test("session_ended error reconciles without becoming a generic failure")
    func staleInteractionReconciles() async throws {
        let client = StatefulWorkspacesClient()
        let (model, runtime) = try await makeLoadedModel(client: client)
        let state = WindowState.ephemeral()
        let workspace = WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_a")
        let selected = SessionRef(connectionID: runtime.id, sessionID: "ses_running")
        #expect(state.activateWorkspace(workspace, in: model))
        #expect(state.selectSession(selected, in: model))

        client.endSession(id: selected.sessionID)
        var actionError: String? = "old error"
        let errorBinding = Binding<String?>(
            get: { actionError },
            set: { actionError = $0 }
        )
        model.run(on: runtime.id, reporting: errorBinding) {
            throw ATCError.api(
                code: "session_ended",
                message: "session has ended",
                sessionID: selected.sessionID
            )
        }
        for _ in 0..<100 where model.session(for: selected)?.status != .ended {
            try await Task.sleep(for: .milliseconds(10))
        }
        state.reconcile(in: model)

        #expect(actionError == nil)
        #expect(model.session(for: selected)?.status == .ended)
        #expect(model.terminals[selected] == nil)
        #expect(state.selectedContent == .session(selected))
    }

    @Test("losing the Active Workspace dismisses the start-session sheet")
    func startSheetDismissedWhenWorkspaceGone() async throws {
        let (model, runtime) = try await makeLoadedModel()
        let (selectionMemory, _) = memory()
        let state = WindowState(selectionMemory: selectionMemory)
        let workspace = WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_parser")
        #expect(state.activateWorkspace(workspace, in: model))
        state.startSessionKind = .agentSession

        model.removeConnection(id: runtime.id)
        state.reconcile(in: model)
        #expect(state.activeWorkspace == nil)
        #expect(state.startSessionKind == nil)
    }

    @Test("selecting an Ended session writes restoration memory")
    func endedSelectionIsRemembered() async throws {
        let (model, runtime) = try await makeLoadedModel()
        let (selectionMemory, _) = memory()
        let state = WindowState(selectionMemory: selectionMemory)
        let workspace = WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_parser")
        let ended = SessionRef(connectionID: runtime.id, sessionID: "ses_abandoned")

        #expect(state.activateWorkspace(workspace, in: model))
        #expect(state.selectSession(ended, in: model))
        #expect(state.selectedContent == .session(ended))
        #expect(selectionMemory.sessionID(for: workspace) == "ses_abandoned")
        #expect(model.terminals[ended] == nil)
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
        let state = WindowState.ephemeral()
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
        let state = WindowState.ephemeral()
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
        let state = WindowState.ephemeral()
        let workspace = WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_a")
        #expect(state.activateWorkspace(workspace, in: model))
        try await runtime.workspaces.delete(id: workspace.workspaceID)
        state.reconcile(in: model)
        #expect(state.activeWorkspace == nil)
        #expect(state.selectedNavigator == .projects)
        #expect(state.selectedContent == .dashboard)
    }

    @Test("creation availability depends on Active Workspace and reachability")
    func creationAvailability() async throws {
        let client = StatefulWorkspacesClient()
        let (model, runtime) = try await makeLoadedModel(client: client)
        let state = WindowState.ephemeral()
        #expect(state.activateWorkspace(
            WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_a"), in: model
        ))
        state.showDashboard()
        #expect(state.canStartSession(in: model))

        client.failSessions = true
        await runtime.refresh()
        #expect(!state.canStartSession(in: model))
    }

    @Test("New Workspace uses Active Project even while Dashboard is visible")
    func createWorkspaceContextUsesActiveWorkspace() async throws {
        let (model, runtime) = try await makeLoadedModel()
        let state = WindowState.ephemeral()
        #expect(state.activateWorkspace(
            WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_parser"), in: model
        ))
        state.showDashboard()
        state.presentCreateWorkspace(in: model)
        #expect(state.createWorkspaceContext?.mode == .preselected(
            ProjectRef(connectionID: runtime.id, projectID: "prj_atelier")
        ))
    }

    @Test("captured creation targets revalidate reachability")
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
    }

    @Test("selection memory is Connection-qualified")
    func selectionMemoryIsComposite() {
        let (selectionMemory, _) = memory()
        let a = WorkspaceRef(connectionID: UUID(), workspaceID: "same")
        let b = WorkspaceRef(connectionID: UUID(), workspaceID: "same")

        selectionMemory.remember(sessionID: "ses_a", for: a)
        selectionMemory.remember(sessionID: "ses_b", for: b)
        #expect(selectionMemory.sessionID(for: a) == "ses_a")
        #expect(selectionMemory.sessionID(for: b) == "ses_b")

        selectionMemory.forget(a)
        #expect(selectionMemory.sessionID(for: a) == nil)
        #expect(selectionMemory.sessionID(for: b) == "ses_b")
    }

    @Test("restoration accepts Live and Ended sessions but rejects missing or moved sessions")
    func selectionRestoreFallbacks() {
        let (memory, _) = memory()
        let ref = WorkspaceRef(connectionID: UUID(), workspaceID: "wsp_a")
        var session = Session(
            id: "ses_1", environment: "host", workingDir: "/home/dev",
            status: .live,
            createdAt: .now, updatedAt: .now,
            workspace: SessionWorkspace(id: "wsp_a", name: "A")
        )
        memory.remember(sessionID: session.id, for: ref)
        for status in [SessionStatus.live, .ended] {
            session.status = status
            #expect(memory.restoredSelection(for: ref, in: [session]) != nil)
        }
        #expect(memory.restoredSelection(for: ref, in: []) == nil)

        session.workspace = SessionWorkspace(id: "wsp_b", name: "B")
        #expect(memory.restoredSelection(for: ref, in: [session]) == nil)
        session.workspace = SessionWorkspace(id: "wsp_a", name: "A")
        #expect(memory.restoredSelection(for: ref, in: [session]) != nil)
    }

    @Test("every delete confirmation names its target and preserves file copy")
    func deleteConfirmationCopy() {
        let live = DeleteConfirmation.sessionMessage(displayName: "Fix parser", status: .live)
        #expect(live.contains("running process will end"))
        #expect(live.hasSuffix(DeleteConfirmation.filesUntouched))
        let ended = DeleteConfirmation.sessionMessage(displayName: "Fix parser", status: .ended)
        #expect(ended.contains("permanently removed"))
        #expect(ended.hasSuffix(DeleteConfirmation.filesUntouched))
        let workspace = DeleteConfirmation.workspaceMessage(
            name: "Spike", sessionCount: 3, activeCount: 2
        )
        #expect(workspace.contains("2 running sessions will be stopped."))
        #expect(workspace.hasSuffix(DeleteConfirmation.filesUntouched))
        #expect(DeleteConfirmation.projectMessage(name: "Atelier")
            .hasSuffix(DeleteConfirmation.filesUntouched))
    }

    @Test("session delete wrapper updates the store")
    func sessionWrappers() async throws {
        let model = makeModel(client: StatefulWorkspacesClient())
        let record = try model.addConnection(name: "A", urlString: "http://a:1", token: "")
        let runtime = try #require(model.runtime(id: record.id))
        runtime.stopPolling()
        await runtime.refresh()
        try await runtime.sessions.delete(id: "ses_done")
        #expect(runtime.sessions.session(id: "ses_done") == nil)
    }
}

import Foundation
import Testing
import ATCAPI
@testable import ATC

/// A client that records project-create and session-start calls (forwarding
/// everything else to a plain mock), so tests can prove a creation flow
/// routed through the intended runtime's client and no other.
private nonisolated final class RecordingClient: ATCClient, @unchecked Sendable {
    private let inner = MockATCClient()
    private let lock = NSLock()
    private var _createdProjects: [(name: String, workingDir: String)] = []
    private var _startedSessions: [StartSessionRequest] = []

    var createdProjects: [(name: String, workingDir: String)] { lock.withLock { _createdProjects } }
    var startedSessions: [StartSessionRequest] { lock.withLock { _startedSessions } }

    func createProject(name: String, workingDir: String) async throws -> Project {
        lock.withLock { _createdProjects.append((name, workingDir)) }
        return try await inner.createProject(name: name, workingDir: workingDir)
    }
    func startSession(_ request: StartSessionRequest) async throws -> Session {
        lock.withLock { _startedSessions.append(request) }
        return try await inner.startSession(request)
    }

    // Everything else is a straight passthrough.
    func health() async throws -> Health { try await inner.health() }
    func version() async throws -> Version { try await inner.version() }
    func sessions(status: SessionStatus?) async throws -> [Session] {
        try await inner.sessions(status: status)
    }
    func session(id: String) async throws -> Session { try await inner.session(id: id) }
    func renameSession(id: String, name: String?) async throws -> Session {
        try await inner.renameSession(id: id, name: name)
    }
    func deleteSession(id: String) async throws { try await inner.deleteSession(id: id) }
    func sendText(sessionID: String, text: String) async throws {}
    func sendKey(sessionID: String, key: String) async throws {}
    func actions() async throws -> [ATCAction] { try await inner.actions() }
    func action(id: String) async throws -> ATCAction { try await inner.action(id: id) }
    func createAction(_ request: ActionCreate) async throws -> ATCAction {
        try await inner.createAction(request)
    }
    func updateAction(id: String, _ request: ActionPatch) async throws -> ATCAction {
        try await inner.updateAction(id: id, request)
    }
    func deleteAction(id: String) async throws { try await inner.deleteAction(id: id) }
    func listDirectory(path: String, showHidden: Bool) async throws -> DirectoryListing {
        try await inner.listDirectory(path: path, showHidden: showHidden)
    }
    func projects() async throws -> [Project] {
        try await inner.projects()
    }
    func project(id: String) async throws -> Project { try await inner.project(id: id) }
    func renameProject(id: String, name: String) async throws -> Project {
        try await inner.renameProject(id: id, name: name)
    }
    func projectSessions(projectID: String, status: SessionStatus?) async throws -> [Session] {
        try await inner.projectSessions(projectID: projectID, status: status)
    }
    func deleteProject(id: String) async throws { try await inner.deleteProject(id: id) }
    func workspaces(projectID: String?) async throws -> [Workspace] {
        try await inner.workspaces(projectID: projectID)
    }
    func workspace(id: String) async throws -> Workspace { try await inner.workspace(id: id) }
    func createWorkspace(projectID: String, name: String) async throws -> Workspace {
        try await inner.createWorkspace(projectID: projectID, name: name)
    }
    func renameWorkspace(id: String, name: String) async throws -> Workspace {
        try await inner.renameWorkspace(id: id, name: name)
    }
    func deleteWorkspace(id: String) async throws { try await inner.deleteWorkspace(id: id) }
    func workspaceSessions(workspaceID: String, status: SessionStatus?) async throws -> [Session] {
        try await inner.workspaceSessions(workspaceID: workspaceID, status: status)
    }
    func attachURL(sessionID: String) -> URL { inner.attachURL(sessionID: sessionID) }
    func attachHeaders() -> [String: String] { inner.attachHeaders() }
}

/// New Project selector behavior and cross-runtime routing of the creation
/// flows (Phase 5).
@MainActor
@Suite("Create project flow")
struct CreateProjectFlowTests {
    // MARK: - Draft selection behavior

    @Test("preselection picks the first connection in creation order")
    func preselectionUsesFirstRuntime() {
        let model = AppModel.preview(connections: [
            (name: "A", client: MockATCClient()),
            (name: "B", client: MockATCClient()),
        ])
        let draft = CreateProjectDraft()
        draft.preselectFirst(in: model.runtimes)
        #expect(draft.connectionID == model.runtimes.first?.id)
    }

    @Test("changing the connection clears the folder but keeps the name")
    func changingConnectionClearsFolderKeepsName() {
        let model = AppModel.preview(connections: [
            (name: "A", client: MockATCClient()),
            (name: "B", client: MockATCClient()),
        ])
        let draft = CreateProjectDraft()
        draft.preselectFirst(in: model.runtimes)
        draft.name = "My Project"
        draft.workingDir = "/home/dev/Projects/atelier"

        draft.selectConnection(model.runtimes[1].id)
        #expect(draft.connectionID == model.runtimes[1].id)
        #expect(draft.workingDir.isEmpty)
        #expect(draft.name == "My Project")
    }

    @Test("reselecting the same connection leaves the folder intact")
    func reselectingSameConnectionKeepsFolder() {
        let model = AppModel.preview(connections: [(name: "A", client: MockATCClient())])
        let draft = CreateProjectDraft()
        draft.preselectFirst(in: model.runtimes)
        draft.workingDir = "/home/dev/Projects/atelier"
        draft.selectConnection(draft.connectionID)
        #expect(draft.workingDir == "/home/dev/Projects/atelier")
    }

    @Test("with no connections there is nothing to select or browse")
    func noConnectionsLeavesSelectionEmpty() {
        let suite = "CreateProjectFlowTests.\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        defaults.removePersistentDomain(forName: suite)
        let model = AppModel(
            connections: ConnectionsStore(defaults: defaults, credentials: InMemoryCredentialStore()),
            clientFactory: { _ in MockATCClient() }
        )
        #expect(model.runtimes.isEmpty)

        let draft = CreateProjectDraft()
        draft.preselectFirst(in: model.runtimes)
        // Nil selection is what disables the folder button in the sheet.
        #expect(draft.connectionID == nil)
        #expect(draft.connectionID.flatMap { model.runtime(id: $0) } == nil)
    }

    // MARK: - Routing to the owning runtime's client

    @Test("project creation routes to the selected runtime's client only")
    func projectCreateRoutesToSelectedRuntime() async throws {
        let clientA = RecordingClient()
        let clientB = RecordingClient()
        let model = AppModel.preview(connections: [
            (name: "A", client: clientA),
            (name: "B", client: clientB),
        ])
        let runtimeB = model.runtimes[1]
        _ = try await runtimeB.projects.create(name: "New", workingDir: "/home/dev/Projects/new")

        #expect(clientB.createdProjects.count == 1)
        #expect(clientB.createdProjects.first?.name == "New")
        #expect(clientB.createdProjects.first?.workingDir == "/home/dev/Projects/new")
        #expect(clientA.createdProjects.isEmpty)
    }

    @Test("session start routes to the target project's runtime client only")
    func sessionStartRoutesToOwningRuntime() async throws {
        let clientA = RecordingClient()
        let clientB = RecordingClient()
        let model = AppModel.preview(connections: [
            (name: "A", client: clientA),
            (name: "B", client: clientB),
        ])
        let runtimeB = model.runtimes[1]
        let request = StartSessionRequest(
            workspaceId: "wsp_parser",
            actionId: "act_vpj2tlg9viqd8ms52ptuvao5c4",
            name: nil
        )
        _ = try await runtimeB.sessions.start(request)

        #expect(clientB.startedSessions.count == 1)
        #expect(clientB.startedSessions.first?.workspaceId == "wsp_parser")
        #expect(clientB.startedSessions.first?.actionId == "act_vpj2tlg9viqd8ms52ptuvao5c4")
        #expect(clientA.startedSessions.isEmpty)
    }
}

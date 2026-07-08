import Foundation
import Testing
import CockpitAPI
@testable import AtelierCode

/// A client that records project-create and session-start calls (forwarding
/// everything else to a plain mock), so tests can prove a creation flow
/// routed through the intended runtime's client and no other.
private nonisolated final class RecordingClient: CockpitClient, @unchecked Sendable {
    private let inner = MockCockpitClient()
    private let lock = NSLock()
    private var _createdProjects: [(name: String, workingDir: String)] = []
    private var _startedSessions: [StartSessionRequest] = []

    var createdProjects: [(name: String, workingDir: String)] { lock.withLock { _createdProjects } }
    var startedSessions: [StartSessionRequest] { lock.withLock { _startedSessions } }

    func createProject(name: String, workingDir: String) async throws -> Project {
        lock.withLock { _createdProjects.append((name, workingDir)) }
        return try await inner.createProject(name: name, workingDir: workingDir)
    }
    func startSession(_ request: StartSessionRequest) async throws -> SessionDetail {
        lock.withLock { _startedSessions.append(request) }
        return try await inner.startSession(request)
    }

    // Everything else is a straight passthrough.
    func health() async throws -> Health { try await inner.health() }
    func version() async throws -> Version { try await inner.version() }
    func sessions(includeArchived: Bool, status: SessionStatus?) async throws -> [Session] {
        try await inner.sessions(includeArchived: includeArchived, status: status)
    }
    func session(id: String) async throws -> SessionDetail { try await inner.session(id: id) }
    func terminateSession(id: String) async throws -> SessionDetail { try await inner.terminateSession(id: id) }
    func archiveSession(id: String) async throws -> SessionDetail { try await inner.archiveSession(id: id) }
    func sendText(sessionID: String, text: String) async throws {}
    func sendKey(sessionID: String, key: String) async throws {}
    func actions() async throws -> [CockpitAction] { try await inner.actions() }
    func action(name: String) async throws -> CockpitAction { try await inner.action(name: name) }
    func createAction(_ request: ActionWriteRequest) async throws -> CockpitAction {
        try await inner.createAction(request)
    }
    func updateAction(name: String, _ request: ActionWriteRequest) async throws -> CockpitAction {
        try await inner.updateAction(name: name, request)
    }
    func setActionEnabled(name: String, enabled: Bool) async throws -> CockpitAction {
        try await inner.setActionEnabled(name: name, enabled: enabled)
    }
    func deleteAction(name: String) async throws { try await inner.deleteAction(name: name) }
    func environments() async throws -> [CockpitEnvironment] { try await inner.environments() }
    func listDirectory(path: String, showHidden: Bool) async throws -> DirectoryListing {
        try await inner.listDirectory(path: path, showHidden: showHidden)
    }
    func projects(includeArchived: Bool) async throws -> [Project] {
        try await inner.projects(includeArchived: includeArchived)
    }
    func project(id: String) async throws -> Project { try await inner.project(id: id) }
    func renameProject(id: String, name: String) async throws -> Project {
        try await inner.renameProject(id: id, name: name)
    }
    func archiveProject(id: String) async throws -> Project { try await inner.archiveProject(id: id) }
    func unarchiveProject(id: String) async throws -> Project { try await inner.unarchiveProject(id: id) }
    func projectSessions(projectID: String, includeArchived: Bool, status: SessionStatus?) async throws -> [Session] {
        try await inner.projectSessions(projectID: projectID, includeArchived: includeArchived, status: status)
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
            (name: "A", client: MockCockpitClient()),
            (name: "B", client: MockCockpitClient()),
        ])
        let draft = CreateProjectDraft()
        draft.preselectFirst(in: model.runtimes)
        #expect(draft.connectionID == model.runtimes.first?.id)
    }

    @Test("changing the connection clears the folder but keeps the name")
    func changingConnectionClearsFolderKeepsName() {
        let model = AppModel.preview(connections: [
            (name: "A", client: MockCockpitClient()),
            (name: "B", client: MockCockpitClient()),
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
        let model = AppModel.preview(connections: [(name: "A", client: MockCockpitClient())])
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
            connections: ConnectionsStore(defaults: defaults),
            clientFactory: { _ in MockCockpitClient() }
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
        let request = StartSessionRequest(action: "claude", projectId: "prj_atelier", name: nil)
        _ = try await runtimeB.sessions.start(request)

        #expect(clientB.startedSessions.count == 1)
        #expect(clientB.startedSessions.first?.projectId == "prj_atelier")
        #expect(clientA.startedSessions.isEmpty)
    }
}

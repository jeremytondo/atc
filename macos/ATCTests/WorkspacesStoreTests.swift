import Foundation
import Testing
import ATCAPI
@testable import ATC

/// WorkspacesStore behavior: refresh, archived-inclusive fetching, and
/// merge-after-mutation semantics.
///
/// Mutation tests use `StatefulWorkspacesClient` for the same reason
/// `ProjectsStoreTests` uses its stateful client: the store fires an
/// unawaited refresh after every mutation, and only a client whose state
/// actually changed keeps those refreshes from resurrecting pre-mutation
/// fixtures mid-test.
@Suite("WorkspacesStore")
struct WorkspacesStoreTests {
    @Test("refresh loads every workspace, archived included")
    func refreshIncludesArchived() async throws {
        let store = WorkspacesStore(client: StatefulWorkspacesClient())
        await store.refresh()
        #expect(store.hasLoadedOnce)
        #expect(store.lastError == nil)
        #expect(store.workspaces.count == 3)
        #expect(store.workspaces.contains { $0.isArchived })
    }

    @Test("create merges the new workspace at the front with a unique id")
    func createMerges() async throws {
        let store = WorkspacesStore(client: StatefulWorkspacesClient())
        await store.refresh()
        let first = try await store.create(projectID: "prj_one", name: "Fresh")
        let second = try await store.create(projectID: "prj_one", name: "Fresher")
        #expect(first.id != second.id)
        #expect(store.workspaces.first?.id == second.id)
        #expect(store.workspaces.contains { $0.id == first.id })
    }

    @Test("creating in an archived project surfaces the server 409")
    func createInArchivedProjectThrows() async throws {
        let store = WorkspacesStore(client: StatefulWorkspacesClient())
        await store.refresh()
        await #expect(throws: ATCError.self) {
            try await store.create(projectID: "prj_archived", name: "Nope")
        }
    }

    @Test("rename updates the workspace in place")
    func renameUpdates() async throws {
        let store = WorkspacesStore(client: StatefulWorkspacesClient())
        await store.refresh()
        let target = try #require(store.workspaces.first)
        try await store.rename(id: target.id, name: "Renamed")
        #expect(store.workspace(id: target.id)?.name == "Renamed")
    }

    @Test("archive keeps the row; views hide it locally")
    func archiveKeepsRow() async throws {
        let store = WorkspacesStore(client: StatefulWorkspacesClient())
        await store.refresh()
        let target = try #require(store.workspaces.first { !$0.isArchived })
        try await store.archive(id: target.id)
        #expect(store.workspace(id: target.id)?.isArchived == true)
        try await store.unarchive(id: target.id)
        #expect(store.workspace(id: target.id)?.isArchived == false)
    }

    @Test("delete removes the row locally")
    func deleteRemoves() async throws {
        let store = WorkspacesStore(client: StatefulWorkspacesClient())
        await store.refresh()
        let target = try #require(store.workspaces.first)
        try await store.delete(id: target.id)
        #expect(store.workspace(id: target.id) == nil)
    }

    @Test("a failed delete (scripted stop error) leaves every row intact")
    func failedDeleteKeepsRows() async throws {
        let client = StatefulWorkspacesClient()
        client.failDeletes = true
        let store = WorkspacesStore(client: client)
        await store.refresh()
        let before = store.workspaces.map(\.id)
        let target = try #require(store.workspaces.first)
        await #expect(throws: ATCError.self) {
            try await store.delete(id: target.id)
        }
        #expect(store.workspaces.map(\.id) == before)
    }

    @Test("failed refresh keeps the error and empty list")
    func refreshError() async throws {
        let store = WorkspacesStore(client: StatefulWorkspacesClient(failAll: true))
        await store.refresh()
        #expect(store.lastError != nil)
        #expect(store.workspaces.isEmpty)
    }
}

/// Minimal stateful in-memory server for workspace tests: mutations
/// persist so the store's follow-up refreshes converge instead of racing
/// the assertions. Sessions and actions pass through to `MockATCClient`;
/// only the workspace and project surfaces are stateful.
final class StatefulWorkspacesClient: ATCClient, @unchecked Sendable {
    private let inner = MockATCClient()
    private let lock = NSLock()
    private var stored: [Workspace]
    private var counter = 0
    private let failAll: Bool
    private var _failDeletes = false
    private var _failSessions = false
    /// Session mutations persist as overrides on the inner fixtures, so
    /// the store's follow-up refreshes converge instead of resurrecting
    /// pre-mutation state.
    private var deletedSessions: Set<String> = []
    private var unarchivedSessions: Set<String> = []

    var failDeletes: Bool {
        get { lock.withLock { _failDeletes } }
        set { lock.withLock { _failDeletes = newValue } }
    }

    /// Failing the sessions fetch flips the whole connection unreachable
    /// (reachability requires every fetch in the combined refresh to
    /// succeed).
    var failSessions: Bool {
        get { lock.withLock { _failSessions } }
        set { lock.withLock { _failSessions = newValue } }
    }

    private let projects = [
        Project(id: "prj_one", name: "One", workingDir: "/home/dev/one", createdAt: .now, updatedAt: .now),
        Project(
            id: "prj_archived", name: "Dusty", workingDir: "/home/dev/dusty",
            createdAt: .now, updatedAt: .now, archivedAt: .now
        ),
    ]

    init(failAll: Bool = false) {
        self.failAll = failAll
        self.stored = [
            Workspace(
                id: "wsp_a", projectId: "prj_one", name: "Alpha",
                createdAt: Date(timeIntervalSinceNow: -100), updatedAt: .now
            ),
            Workspace(
                id: "wsp_b", projectId: "prj_one", name: "Beta",
                createdAt: Date(timeIntervalSinceNow: -200), updatedAt: .now
            ),
            Workspace(
                id: "wsp_old", projectId: "prj_one", name: "Old",
                createdAt: Date(timeIntervalSinceNow: -300), updatedAt: .now,
                archivedAt: .now
            ),
        ]
    }

    private func withState<T>(_ body: (inout [Workspace]) throws -> T) throws -> T {
        if failAll { throw ATCError.badStatus(500) }
        lock.lock()
        defer { lock.unlock() }
        return try body(&stored)
    }

    func workspaces(projectID: String?, includeArchived: Bool) async throws -> [Workspace] {
        try withState { state in
            state.filter {
                (projectID == nil || $0.projectId == projectID)
                    && (includeArchived || !$0.isArchived)
            }
        }
    }

    func workspace(id: String) async throws -> Workspace {
        try withState { state in
            guard let found = state.first(where: { $0.id == id }) else {
                throw ATCError.api(code: "workspace_not_found", message: id, sessionID: nil)
            }
            return found
        }
    }

    func createWorkspace(projectID: String, name: String) async throws -> Workspace {
        guard let project = projects.first(where: { $0.id == projectID }) else {
            throw ATCError.api(code: "project_not_found", message: projectID, sessionID: nil)
        }
        guard !project.isArchived else {
            throw ATCError.api(code: "project_archived", message: projectID, sessionID: nil)
        }
        return try withState { state in
            counter += 1
            let workspace = Workspace(
                id: "wsp_stateful_\(counter)", projectId: projectID, name: name,
                createdAt: .now, updatedAt: .now
            )
            state.insert(workspace, at: 0)
            return workspace
        }
    }

    func renameWorkspace(id: String, name: String) async throws -> Workspace {
        try mutate(id) { $0.name = name }
    }

    func archiveWorkspace(id: String) async throws -> Workspace {
        try mutate(id) { $0.archivedAt = .now }
    }

    func unarchiveWorkspace(id: String) async throws -> Workspace {
        try mutate(id) { $0.archivedAt = nil }
    }

    func deleteWorkspace(id: String) async throws {
        if failDeletes {
            // The server's shape for "a session refused to stop".
            throw ATCError.badStatus(502)
        }
        try withState { state in
            guard state.contains(where: { $0.id == id }) else {
                throw ATCError.api(code: "workspace_not_found", message: id, sessionID: nil)
            }
            state.removeAll { $0.id == id }
        }
    }

    private func mutate(_ id: String, _ change: (inout Workspace) -> Void) throws -> Workspace {
        try withState { state in
            guard let index = state.firstIndex(where: { $0.id == id }) else {
                throw ATCError.api(code: "workspace_not_found", message: id, sessionID: nil)
            }
            change(&state[index])
            state[index].updatedAt = .now
            return state[index]
        }
    }

    // MARK: - Pass-through / unused surface

    func projects(includeArchived: Bool) async throws -> [Project] {
        if failAll { throw ATCError.badStatus(500) }
        return projects.filter { includeArchived || !$0.isArchived }
    }

    func project(id: String) async throws -> Project {
        guard let project = projects.first(where: { $0.id == id }) else {
            throw ATCError.api(code: "project_not_found", message: id, sessionID: nil)
        }
        return project
    }

    func health() async throws -> Health { try await inner.health() }
    func version() async throws -> Version { try await inner.version() }
    func sessions(includeArchived: Bool, status: SessionStatus?) async throws -> [Session] {
        if failAll || failSessions { throw ATCError.badStatus(500) }
        let overrides = lock.withLock { (deleted: deletedSessions, unarchived: unarchivedSessions) }
        return try await inner.sessions(includeArchived: true, status: status)
            .filter { !overrides.deleted.contains($0.id) }
            .map { session in
                var session = session
                if overrides.unarchived.contains(session.id) {
                    session.archivedAt = nil
                }
                return session
            }
            .filter { includeArchived || !$0.isArchived }
    }
    func session(id: String) async throws -> SessionDetail { try await inner.session(id: id) }
    func startSession(_ request: StartSessionRequest) async throws -> SessionDetail {
        try await inner.startSession(request)
    }
    func terminateSession(id: String) async throws -> SessionDetail {
        try await inner.terminateSession(id: id)
    }
    func archiveSession(id: String) async throws -> SessionDetail {
        try await inner.archiveSession(id: id)
    }
    func unarchiveSession(id: String) async throws -> SessionDetail {
        let detail = try await inner.unarchiveSession(id: id)
        lock.withLock { _ = unarchivedSessions.insert(id) }
        return detail
    }
    func deleteSession(id: String) async throws {
        try await inner.deleteSession(id: id)
        lock.withLock { _ = deletedSessions.insert(id) }
    }
    func sendText(sessionID: String, text: String) async throws {}
    func sendKey(sessionID: String, key: String) async throws {}
    func actions() async throws -> [ATCAction] {
        if failAll { throw ATCError.badStatus(500) }
        return try await inner.actions()
    }
    func action(name: String) async throws -> ATCAction { try await inner.action(name: name) }
    func createAction(_ request: ActionWriteRequest) async throws -> ATCAction {
        try await inner.createAction(request)
    }
    func updateAction(name: String, _ request: ActionWriteRequest) async throws -> ATCAction {
        try await inner.updateAction(name: name, request)
    }
    func setActionEnabled(name: String, enabled: Bool) async throws -> ATCAction {
        try await inner.setActionEnabled(name: name, enabled: enabled)
    }
    func deleteAction(name: String) async throws { try await inner.deleteAction(name: name) }
    func environments() async throws -> [ATCEnvironment] { try await inner.environments() }
    func listDirectory(path: String, showHidden: Bool) async throws -> DirectoryListing {
        try await inner.listDirectory(path: path, showHidden: showHidden)
    }
    func createProject(name: String, workingDir: String) async throws -> Project {
        try await inner.createProject(name: name, workingDir: workingDir)
    }
    func renameProject(id: String, name: String) async throws -> Project {
        try await inner.renameProject(id: id, name: name)
    }
    func archiveProject(id: String) async throws -> Project { try await inner.archiveProject(id: id) }
    func unarchiveProject(id: String) async throws -> Project { try await inner.unarchiveProject(id: id) }
    func projectSessions(projectID: String, includeArchived: Bool, status: SessionStatus?) async throws -> [Session] {
        try await inner.projectSessions(projectID: projectID, includeArchived: includeArchived, status: status)
    }
    func deleteProject(id: String) async throws { try await inner.deleteProject(id: id) }
    func workspaceSessions(workspaceID: String, includeArchived: Bool, status: SessionStatus?) async throws -> [Session] {
        try await inner.workspaceSessions(workspaceID: workspaceID, includeArchived: includeArchived, status: status)
    }
    func attachURL(sessionID: String) -> URL { inner.attachURL(sessionID: sessionID) }
    func attachHeaders() -> [String: String] { inner.attachHeaders() }
}

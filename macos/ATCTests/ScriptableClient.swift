import Foundation
import ATCAPI
@testable import ATC

/// A client whose calls can be failed on demand and/or delayed — drives
/// reachability transitions and edit-during-refresh races.
nonisolated final class ScriptableClient: ATCClient, @unchecked Sendable {
    private let inner = MockATCClient()
    private let lock = NSLock()
    private var _shouldFail = false
    private var _delay: Duration?

    var shouldFail: Bool {
        get { lock.withLock { _shouldFail } }
        set { lock.withLock { _shouldFail = newValue } }
    }

    var delay: Duration? {
        get { lock.withLock { _delay } }
        set { lock.withLock { _delay = newValue } }
    }

    private func gate() async throws {
        if let delay {
            try? await Task.sleep(for: delay)
        }
        if shouldFail {
            throw ATCError.api(code: "unreachable", message: "scripted failure", sessionID: nil)
        }
    }

    func health() async throws -> Health { try await gate(); return try await inner.health() }
    func version() async throws -> Version { try await gate(); return try await inner.version() }
    func sessions(status: SessionStatus?) async throws -> [Session] {
        try await gate()
        return try await inner.sessions(status: status)
    }
    func session(id: String) async throws -> Session {
        try await gate(); return try await inner.session(id: id)
    }
    func startSession(_ request: StartSessionRequest) async throws -> Session {
        try await gate(); return try await inner.startSession(request)
    }
    func renameSession(id: String, name: String?) async throws -> Session {
        try await gate(); return try await inner.renameSession(id: id, name: name)
    }
    func deleteSession(id: String) async throws {
        try await gate(); try await inner.deleteSession(id: id)
    }
    func sendText(sessionID: String, text: String) async throws { try await gate() }
    func sendKey(sessionID: String, key: String) async throws { try await gate() }
    func actions() async throws -> [ATCAction] { try await gate(); return try await inner.actions() }
    func action(id: String) async throws -> ATCAction {
        try await gate(); return try await inner.action(id: id)
    }
    func createAction(_ request: ActionCreate) async throws -> ATCAction {
        try await gate(); return try await inner.createAction(request)
    }
    func updateAction(id: String, _ request: ActionPatch) async throws -> ATCAction {
        try await gate(); return try await inner.updateAction(id: id, request)
    }
    func deleteAction(id: String) async throws {
        try await gate(); try await inner.deleteAction(id: id)
    }
    func listDirectory(path: String, showHidden: Bool) async throws -> DirectoryListing {
        try await gate(); return try await inner.listDirectory(path: path, showHidden: showHidden)
    }
    func projects() async throws -> [Project] {
        try await gate(); return try await inner.projects()
    }
    func project(id: String) async throws -> Project { try await gate(); return try await inner.project(id: id) }
    func createProject(name: String, workingDir: String) async throws -> Project {
        try await gate(); return try await inner.createProject(name: name, workingDir: workingDir)
    }
    func renameProject(id: String, name: String) async throws -> Project {
        try await gate(); return try await inner.renameProject(id: id, name: name)
    }
    func projectSessions(projectID: String, status: SessionStatus?) async throws -> [Session] {
        try await gate()
        return try await inner.projectSessions(
            projectID: projectID, status: status
        )
    }
    func deleteProject(id: String) async throws {
        try await gate(); try await inner.deleteProject(id: id)
    }
    func workspaces(projectID: String?) async throws -> [Workspace] {
        try await gate()
        return try await inner.workspaces(projectID: projectID)
    }
    func workspace(id: String) async throws -> Workspace {
        try await gate(); return try await inner.workspace(id: id)
    }
    func createWorkspace(projectID: String, name: String) async throws -> Workspace {
        try await gate(); return try await inner.createWorkspace(projectID: projectID, name: name)
    }
    func renameWorkspace(id: String, name: String) async throws -> Workspace {
        try await gate(); return try await inner.renameWorkspace(id: id, name: name)
    }
    func deleteWorkspace(id: String) async throws {
        try await gate(); try await inner.deleteWorkspace(id: id)
    }
    func workspaceSessions(workspaceID: String, status: SessionStatus?) async throws -> [Session] {
        try await gate()
        return try await inner.workspaceSessions(
            workspaceID: workspaceID, status: status
        )
    }
    func attachURL(sessionID: String) -> URL { inner.attachURL(sessionID: sessionID) }
    func attachHeaders() -> [String: String] { inner.attachHeaders() }
}

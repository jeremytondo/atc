import Foundation

/// The interface the app depends on for all atc REST calls.
/// `HTTPATCClient` is the real implementation; previews and tests
/// inject mocks.
public protocol ATCClient: Sendable {
    func health() async throws -> Health
    func version() async throws -> Version
    func sessions(status: SessionStatus?) async throws -> [Session]
    func session(id: String) async throws -> SessionDetail
    func startSession(_ request: StartSessionRequest) async throws -> SessionDetail
    func renameSession(id: String, name: String) async throws -> SessionDetail
    /// Deletes a session's metadata, ending it first if Live. Files
    /// are never touched.
    func deleteSession(id: String) async throws
    func sendText(sessionID: String, text: String) async throws
    func sendKey(sessionID: String, key: String) async throws
    func actions() async throws -> [ATCAction]
    func action(name: String) async throws -> ATCAction
    func createAction(_ request: ActionWriteRequest) async throws -> ATCAction
    func updateAction(name: String, _ request: ActionWriteRequest) async throws -> ATCAction
    func setActionEnabled(name: String, enabled: Bool) async throws -> ATCAction
    /// Deletes a custom action; on a built-in override, reverts it to the
    /// built-in default instead (the action keeps existing).
    func deleteAction(name: String) async throws
    func environments() async throws -> [ATCEnvironment]
    func listDirectory(path: String, showHidden: Bool) async throws -> DirectoryListing
    func projects(includeArchived: Bool) async throws -> [Project]
    func project(id: String) async throws -> Project
    func createProject(name: String, workingDir: String) async throws -> Project
    func renameProject(id: String, name: String) async throws -> Project
    func archiveProject(id: String) async throws -> Project
    func unarchiveProject(id: String) async throws -> Project
    /// Deletes a project record; allowed only once it has zero workspaces.
    func deleteProject(id: String) async throws
    func projectSessions(projectID: String, status: SessionStatus?) async throws -> [Session]
    /// Lists workspaces newest-first; a nil `projectID` spans every project.
    func workspaces(projectID: String?, includeArchived: Bool) async throws -> [Workspace]
    func workspace(id: String) async throws -> Workspace
    func createWorkspace(projectID: String, name: String) async throws -> Workspace
    func renameWorkspace(id: String, name: String) async throws -> Workspace
    func archiveWorkspace(id: String) async throws -> Workspace
    func unarchiveWorkspace(id: String) async throws -> Workspace
    /// Deletes a workspace and its sessions' metadata, ending Live sessions
    /// first. Files are never touched.
    func deleteWorkspace(id: String) async throws
    func workspaceSessions(workspaceID: String, status: SessionStatus?) async throws -> [Session]

    /// WebSocket URL for `GET /api/sessions/{id}/attach`.
    func attachURL(sessionID: String) -> URL
    /// Extra headers (e.g. bearer auth) the attach WebSocket must send.
    func attachHeaders() -> [String: String]
}

public extension ATCClient {
    func sessions() async throws -> [Session] {
        try await sessions(status: nil)
    }

    func listDirectory(path: String) async throws -> DirectoryListing {
        try await listDirectory(path: path, showHidden: false)
    }

    func projects() async throws -> [Project] {
        try await projects(includeArchived: false)
    }

    func projectSessions(projectID: String) async throws -> [Session] {
        try await projectSessions(projectID: projectID, status: nil)
    }

    func workspaces(projectID: String? = nil) async throws -> [Workspace] {
        try await workspaces(projectID: projectID, includeArchived: false)
    }

    func workspaceSessions(workspaceID: String) async throws -> [Session] {
        try await workspaceSessions(workspaceID: workspaceID, status: nil)
    }
}

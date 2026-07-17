import Foundation

/// URLSession-backed implementation of `ATCClient`.
public struct HTTPATCClient: ATCClient {
    public let server: ATCServer
    private let session: URLSession
    private let decoder: JSONDecoder

    public init(server: ATCServer, session: URLSession = .shared) {
        self.server = server
        self.session = session
        self.decoder = .atc()
    }

    // MARK: - ATCClient

    public func health() async throws -> Health {
        try await get("health")
    }

    public func version() async throws -> Version {
        try await get("version")
    }

    public func sessions(status: SessionStatus?) async throws -> [Session] {
        var query: [URLQueryItem] = []
        if let status {
            query.append(URLQueryItem(name: "status", value: status.rawValue))
        }
        let envelope: SessionsEnvelope = try await get("sessions", query: query)
        return envelope.sessions
    }

    public func session(id: String) async throws -> SessionDetail {
        try await get("sessions/\(id)")
    }

    public func startSession(_ request: StartSessionRequest) async throws -> SessionDetail {
        try await post("sessions/start", body: request)
    }

    public func deleteSession(id: String) async throws {
        _ = try await send(method: "DELETE", path: "sessions/\(id)", body: nil as Never?)
    }

    public func sendText(sessionID: String, text: String) async throws {
        struct Body: Encodable { var text: String }
        _ = try await send(method: "POST", path: "sessions/\(sessionID)/send-text", body: Body(text: text))
    }

    public func sendKey(sessionID: String, key: String) async throws {
        struct Body: Encodable { var key: String }
        _ = try await send(method: "POST", path: "sessions/\(sessionID)/send-key", body: Body(key: key))
    }

    public func actions() async throws -> [ATCAction] {
        let envelope: ActionsEnvelope = try await get("actions")
        return envelope.actions
    }

    public func action(name: String) async throws -> ATCAction {
        try await get("actions/\(name)")
    }

    public func createAction(_ request: ActionWriteRequest) async throws -> ATCAction {
        try await post("actions", body: request)
    }

    public func updateAction(name: String, _ request: ActionWriteRequest) async throws -> ATCAction {
        try await put("actions/\(name)", body: request)
    }

    public func setActionEnabled(name: String, enabled: Bool) async throws -> ATCAction {
        struct Body: Encodable { var enabled: Bool }
        return try await put("actions/\(name)/enabled", body: Body(enabled: enabled))
    }

    public func deleteAction(name: String) async throws {
        _ = try await send(method: "DELETE", path: "actions/\(name)", body: nil as Never?)
    }

    public func environments() async throws -> [ATCEnvironment] {
        let envelope: EnvironmentsEnvelope = try await get("environments")
        return envelope.environments
    }

    public func listDirectory(path: String, showHidden: Bool) async throws -> DirectoryListing {
        var query = [URLQueryItem(name: "path", value: path)]
        if showHidden {
            query.append(URLQueryItem(name: "showHidden", value: "true"))
        }
        return try await get("fs/list", query: query)
    }

    public func projects(includeArchived: Bool) async throws -> [Project] {
        var query: [URLQueryItem] = []
        if includeArchived {
            query.append(URLQueryItem(name: "includeArchived", value: "true"))
        }
        let envelope: ProjectsEnvelope = try await get("projects", query: query)
        return envelope.projects
    }

    public func project(id: String) async throws -> Project {
        try await get("projects/\(id)")
    }

    public func createProject(name: String, workingDir: String) async throws -> Project {
        struct Body: Encodable { var name: String; var workingDir: String }
        return try await post("projects", body: Body(name: name, workingDir: workingDir))
    }

    public func renameProject(id: String, name: String) async throws -> Project {
        struct Body: Encodable { var name: String }
        return try await patch("projects/\(id)", body: Body(name: name))
    }

    public func archiveProject(id: String) async throws -> Project {
        try await post("projects/\(id)/archive")
    }

    public func unarchiveProject(id: String) async throws -> Project {
        try await post("projects/\(id)/unarchive")
    }

    public func deleteProject(id: String) async throws {
        _ = try await send(method: "DELETE", path: "projects/\(id)", body: nil as Never?)
    }

    public func workspaces(projectID: String?, includeArchived: Bool) async throws -> [Workspace] {
        var query: [URLQueryItem] = []
        if let projectID {
            query.append(URLQueryItem(name: "projectId", value: projectID))
        }
        if includeArchived {
            query.append(URLQueryItem(name: "includeArchived", value: "true"))
        }
        let envelope: WorkspacesEnvelope = try await get("workspaces", query: query)
        return envelope.workspaces
    }

    public func workspace(id: String) async throws -> Workspace {
        try await get("workspaces/\(id)")
    }

    public func createWorkspace(projectID: String, name: String) async throws -> Workspace {
        struct Body: Encodable { var projectId: String; var name: String }
        return try await post("workspaces", body: Body(projectId: projectID, name: name))
    }

    public func renameWorkspace(id: String, name: String) async throws -> Workspace {
        struct Body: Encodable { var name: String }
        return try await patch("workspaces/\(id)", body: Body(name: name))
    }

    public func archiveWorkspace(id: String) async throws -> Workspace {
        try await post("workspaces/\(id)/archive")
    }

    public func unarchiveWorkspace(id: String) async throws -> Workspace {
        try await post("workspaces/\(id)/unarchive")
    }

    public func deleteWorkspace(id: String) async throws {
        _ = try await send(method: "DELETE", path: "workspaces/\(id)", body: nil as Never?)
    }

    public func workspaceSessions(
        workspaceID: String,
        status: SessionStatus?
    ) async throws -> [Session] {
        var query: [URLQueryItem] = []
        if let status {
            query.append(URLQueryItem(name: "status", value: status.rawValue))
        }
        let envelope: SessionsEnvelope = try await get("workspaces/\(workspaceID)/sessions", query: query)
        return envelope.sessions
    }

    public func projectSessions(
        projectID: String,
        status: SessionStatus?
    ) async throws -> [Session] {
        var query: [URLQueryItem] = []
        if let status {
            query.append(URLQueryItem(name: "status", value: status.rawValue))
        }
        let envelope: SessionsEnvelope = try await get("projects/\(projectID)/sessions", query: query)
        return envelope.sessions
    }

    public func attachURL(sessionID: String) -> URL {
        server.attachURL(sessionID: sessionID)
    }

    public func attachHeaders() -> [String: String] {
        server.authHeaders
    }

    // MARK: - Plumbing

    private func get<T: Decodable>(_ path: String, query: [URLQueryItem] = []) async throws -> T {
        let data = try await send(method: "GET", path: path, query: query, body: nil as Never?)
        return try decode(data)
    }

    private func post<T: Decodable>(_ path: String) async throws -> T {
        let data = try await send(method: "POST", path: path, body: nil as Never?)
        return try decode(data)
    }

    private func post<T: Decodable>(_ path: String, body: some Encodable) async throws -> T {
        let data = try await send(method: "POST", path: path, body: body)
        return try decode(data)
    }

    private func patch<T: Decodable>(_ path: String, body: some Encodable) async throws -> T {
        let data = try await send(method: "PATCH", path: path, body: body)
        return try decode(data)
    }

    private func put<T: Decodable>(_ path: String, body: some Encodable) async throws -> T {
        let data = try await send(method: "PUT", path: path, body: body)
        return try decode(data)
    }

    private func decode<T: Decodable>(_ data: Data) throws -> T {
        do {
            return try decoder.decode(T.self, from: data)
        } catch {
            throw ATCError.decoding(underlying: error)
        }
    }

    @discardableResult
    private func send(
        method: String,
        path: String,
        query: [URLQueryItem] = [],
        body: (some Encodable)?
    ) async throws -> Data {
        var request = URLRequest(url: server.restURL(path, query: query))
        request.httpMethod = method
        for (header, value) in server.authHeaders {
            request.setValue(value, forHTTPHeaderField: header)
        }
        if let body {
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
            request.httpBody = try JSONEncoder().encode(body)
        }

        let data: Data
        let response: URLResponse
        do {
            (data, response) = try await session.data(for: request)
        } catch {
            throw ATCError.transport(underlying: error)
        }

        guard let http = response as? HTTPURLResponse else {
            throw ATCError.badStatus(-1)
        }
        guard (200..<300).contains(http.statusCode) else {
            if let envelope = try? decoder.decode(ErrorEnvelope.self, from: data) {
                throw ATCError.api(
                    code: envelope.error,
                    message: envelope.message,
                    sessionID: envelope.sessionId
                )
            }
            throw ATCError.badStatus(http.statusCode)
        }
        return data
    }
}

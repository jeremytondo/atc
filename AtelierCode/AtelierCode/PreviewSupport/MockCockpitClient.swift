import Foundation
import CockpitAPI

/// Canned-data client for previews and offline development.
nonisolated struct MockCockpitClient: CockpitClient {
    var mockSessions: [Session] = [
        Session(
            id: "ses_running",
            name: "Fix the parser",
            action: "claude",
            environment: "host-login-shell",
            workingDir: "/home/dev/projects/cockpit",
            status: .running,
            attachable: true,
            createdAt: Date(timeIntervalSinceNow: -3600),
            updatedAt: Date(timeIntervalSinceNow: -60)
        ),
        Session(
            id: "ses_starting",
            action: "codex",
            environment: "host-login-shell",
            workingDir: "/home/dev/projects/web",
            status: .starting,
            attachable: false,
            createdAt: Date(timeIntervalSinceNow: -30),
            updatedAt: Date(timeIntervalSinceNow: -30)
        ),
        Session(
            id: "ses_failed",
            action: "lazygit",
            environment: "host-login-shell",
            workingDir: "/home/dev",
            status: .failed,
            attachable: false,
            failureReason: "command not found: lazygit",
            failureCode: "spawn_failed",
            createdAt: Date(timeIntervalSinceNow: -7200),
            updatedAt: Date(timeIntervalSinceNow: -7100)
        ),
        Session(
            id: "ses_done",
            name: "Yesterday's refactor",
            action: "claude",
            environment: "host-login-shell",
            workingDir: "/home/dev/projects/cockpit",
            status: .terminated,
            attachable: false,
            createdAt: Date(timeIntervalSinceNow: -90000),
            updatedAt: Date(timeIntervalSinceNow: -86400),
            terminatedAt: Date(timeIntervalSinceNow: -86400)
        ),
    ]

    func health() async throws -> Health { Health(status: "ok") }

    func version() async throws -> Version {
        Version(name: "cockpit", version: "dev", commit: "unknown")
    }

    func sessions(includeArchived: Bool, status: SessionStatus?) async throws -> [Session] {
        mockSessions.filter { includeArchived || !$0.isArchived }
    }

    func session(id: String) async throws -> SessionDetail {
        guard let session = mockSessions.first(where: { $0.id == id }) else {
            throw CockpitError.api(code: "session_not_found", message: "session not found: \(id)", sessionID: id)
        }
        return detail(from: session)
    }

    func startSession(_ request: StartSessionRequest) async throws -> SessionDetail {
        let session = Session(
            id: "ses_new",
            name: request.name,
            action: request.action,
            environment: request.environment ?? "host-login-shell",
            workingDir: request.workingDir,
            status: .starting,
            attachable: false,
            createdAt: Date(),
            updatedAt: Date()
        )
        return detail(from: session)
    }

    func terminateSession(id: String) async throws -> SessionDetail {
        var session = try await self.session(id: id)
        session.status = .terminated
        session.attachable = false
        session.terminatedAt = Date()
        return session
    }

    func archiveSession(id: String) async throws -> SessionDetail {
        var session = try await self.session(id: id)
        guard session.status == .terminated || session.status == .failed else {
            throw CockpitError.api(code: "session_live", message: "session is still running", sessionID: id)
        }
        session.archivedAt = Date()
        return session
    }

    func sendText(sessionID: String, text: String) async throws {}
    func sendKey(sessionID: String, key: String) async throws {}

    func actions() async throws -> [CockpitAction] {
        let json = Data(#"""
        {"actions":[
          {"name":"claude","origin":"builtin","enabled":true,"label":"Claude","description":"Claude Code CLI","prompt":{},"params":{}},
          {"name":"codex","origin":"builtin","enabled":true,"label":"Codex","description":"OpenAI Codex CLI","prompt":{},"params":{
            "model":{"type":"enum","values":["fast","smart"],"default":"fast","flag":"--model","label":"Model"},
            "verbose":{"type":"bool","flag":"--verbose","label":"Verbose"}
          }},
          {"name":"lazygit","origin":"custom","enabled":true,"label":"LazyGit","params":{}}
        ]}
        """#.utf8)
        struct Envelope: Decodable { var actions: [CockpitAction] }
        return try JSONDecoder().decode(Envelope.self, from: json).actions
    }

    func environments() async throws -> [CockpitEnvironment] {
        let json = Data(#"""
        {"environments":[{"name":"host-login-shell","kind":"host-login-shell","label":"Host login shell","default":true}]}
        """#.utf8)
        struct Envelope: Decodable { var environments: [CockpitEnvironment] }
        return try JSONDecoder().decode(Envelope.self, from: json).environments
    }

    func attachURL(sessionID: String) -> URL {
        URL(string: "ws://127.0.0.1:7331/api/sessions/\(sessionID)/attach")!
    }

    func attachHeaders() -> [String: String] { [:] }

    private func detail(from session: Session) -> SessionDetail {
        // Round-trip through Codable to build the detail shape without a
        // giant memberwise call.
        let encoder = JSONEncoder()
        encoder.dateEncodingStrategy = .iso8601
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .iso8601
        let data = try! encoder.encode(session)
        return try! decoder.decode(SessionDetail.self, from: data)
    }
}

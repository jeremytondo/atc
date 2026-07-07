import Foundation
import CockpitAPI

/// Canned-data client for previews and offline development.
nonisolated struct MockCockpitClient: CockpitClient {
    /// Stable-ID project fixtures. Working dirs reuse `mockTree` paths so a
    /// project-scoped session lands in a browsable directory. One archived.
    var mockProjects: [Project] = [
        Project(
            id: "prj_atelier",
            name: "Atelier",
            workingDir: "/home/dev/Projects/atelier",
            createdAt: Date(timeIntervalSinceNow: -400000),
            updatedAt: Date(timeIntervalSinceNow: -60)
        ),
        Project(
            id: "prj_blazerr",
            name: "Blazerr",
            workingDir: "/home/dev/Projects/huge",
            createdAt: Date(timeIntervalSinceNow: -300000),
            updatedAt: Date(timeIntervalSinceNow: -3600)
        ),
        Project(
            id: "prj_scratch",
            name: "Scratch",
            workingDir: "/home/dev/Projects/empty",
            createdAt: Date(timeIntervalSinceNow: -500000),
            updatedAt: Date(timeIntervalSinceNow: -200000),
            archivedAt: Date(timeIntervalSinceNow: -100000)
        ),
    ]

    /// Nested project refs derived from `mockProjects`, for tagging sessions.
    private static let atelierRef = SessionProject(
        id: "prj_atelier", name: "Atelier", workingDir: "/home/dev/Projects/atelier"
    )
    private static let blazerrRef = SessionProject(
        id: "prj_blazerr", name: "Blazerr", workingDir: "/home/dev/Projects/huge"
    )

    var mockSessions: [Session] = [
        Session(
            id: "ses_running",
            name: "Fix the parser",
            action: "claude",
            environment: "host-login-shell",
            workingDir: "/home/dev/Projects/atelier",
            status: .running,
            attachable: true,
            createdAt: Date(timeIntervalSinceNow: -3600),
            updatedAt: Date(timeIntervalSinceNow: -60),
            project: MockCockpitClient.atelierRef
        ),
        Session(
            id: "ses_starting",
            action: "codex",
            environment: "host-login-shell",
            workingDir: "/home/dev/Projects/huge",
            status: .starting,
            attachable: false,
            createdAt: Date(timeIntervalSinceNow: -30),
            updatedAt: Date(timeIntervalSinceNow: -30),
            project: MockCockpitClient.blazerrRef
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
            workingDir: "/home/dev/Projects/atelier",
            status: .terminated,
            attachable: false,
            createdAt: Date(timeIntervalSinceNow: -90000),
            updatedAt: Date(timeIntervalSinceNow: -86400),
            terminatedAt: Date(timeIntervalSinceNow: -86400),
            project: MockCockpitClient.atelierRef
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
        var workingDir = request.workingDir ?? ""
        var projectRef: SessionProject?
        if let projectId = request.projectId {
            guard let project = mockProjects.first(where: { $0.id == projectId }) else {
                throw CockpitError.api(
                    code: "project_not_found", message: "project not found: \(projectId)", sessionID: nil
                )
            }
            guard !project.isArchived else {
                throw CockpitError.api(
                    code: "project_archived", message: "project is archived: \(projectId)", sessionID: nil
                )
            }
            workingDir = project.workingDir
            projectRef = SessionProject(
                id: project.id,
                name: project.name,
                workingDir: project.workingDir,
                archivedAt: project.archivedAt
            )
        }
        let session = Session(
            id: "ses_new",
            name: request.name,
            action: request.action,
            environment: request.environment ?? "host-login-shell",
            workingDir: workingDir,
            status: .starting,
            attachable: false,
            createdAt: Date(),
            updatedAt: Date(),
            project: projectRef
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

    // MARK: - Projects

    func projects(includeArchived: Bool) async throws -> [Project] {
        mockProjects.filter { includeArchived || !$0.isArchived }
    }

    func project(id: String) async throws -> Project {
        try lookupProject(id)
    }

    func createProject(name: String, workingDir: String) async throws -> Project {
        Project(
            id: "prj_new",
            name: name,
            workingDir: workingDir,
            createdAt: Date(),
            updatedAt: Date()
        )
    }

    func renameProject(id: String, name: String) async throws -> Project {
        var project = try lookupProject(id)
        project.name = name
        project.updatedAt = Date()
        return project
    }

    func archiveProject(id: String) async throws -> Project {
        var project = try lookupProject(id)
        project.archivedAt = Date()
        project.updatedAt = Date()
        return project
    }

    func unarchiveProject(id: String) async throws -> Project {
        var project = try lookupProject(id)
        project.archivedAt = nil
        project.updatedAt = Date()
        return project
    }

    func projectSessions(
        projectID: String,
        includeArchived: Bool,
        status: SessionStatus?
    ) async throws -> [Session] {
        _ = try lookupProject(projectID)
        return mockSessions.filter { session in
            session.project?.id == projectID
                && (includeArchived || !session.isArchived)
                && (status == nil || session.status == status)
        }
    }

    private func lookupProject(_ id: String) throws -> Project {
        guard let project = mockProjects.first(where: { $0.id == id }) else {
            throw CockpitError.api(
                code: "project_not_found", message: "project not found: \(id)", sessionID: nil
            )
        }
        return project
    }

    // MARK: - File system

    var mockRoots: [RemoteWorkspaceRoot] = [
        RemoteWorkspaceRoot(label: "Projects", path: "/home/dev/Projects"),
        RemoteWorkspaceRoot(label: "Home", path: "/home/dev"),
    ]

    /// Directory path → full (unfiltered) children. `listDirectory` filters
    /// dot-entries itself and throws typed errors, mimicking the server so
    /// `RemoteFileBrowser` tests cover error handling.
    var mockTree: [String: [RemoteEntry]] = {
        func dir(_ path: String, symlink: Bool = false) -> RemoteEntry {
            RemoteEntry(
                name: (path as NSString).lastPathComponent, path: path,
                kind: .directory, isSymlink: symlink,
                modifiedAt: Date(timeIntervalSinceNow: -3600)
            )
        }
        func file(_ path: String, size: Int64 = 2048, symlink: Bool = false) -> RemoteEntry {
            RemoteEntry(
                name: (path as NSString).lastPathComponent, path: path,
                kind: .file, isSymlink: symlink, size: size,
                modifiedAt: Date(timeIntervalSinceNow: -7200)
            )
        }
        return [
            "/home/dev": [
                dir("/home/dev/.config"),
                dir("/home/dev/Documents"),
                dir("/home/dev/Projects"),
                file("/home/dev/.zshrc", size: 512),
            ],
            "/home/dev/Projects": [
                dir("/home/dev/Projects/atelier"),
                dir("/home/dev/Projects/empty"),
                dir("/home/dev/Projects/huge"),
                dir("/home/dev/Projects/secrets"),
                file("/home/dev/Projects/notes.md", size: 128),
            ],
            "/home/dev/Projects/atelier": [
                dir("/home/dev/Projects/atelier/.git"),
                dir("/home/dev/Projects/atelier/docs"),
                dir("/home/dev/Projects/atelier/shared", symlink: true),
                dir("/home/dev/Projects/atelier/src"),
                file("/home/dev/Projects/atelier/.gitignore", size: 64),
                RemoteEntry(
                    name: "dangling", path: "/home/dev/Projects/atelier/dangling",
                    kind: .unknown, isSymlink: true
                ),
                file("/home/dev/Projects/atelier/LICENSE", symlink: true),
                file("/home/dev/Projects/atelier/README.md"),
            ],
            "/home/dev/Projects/atelier/src": [
                dir("/home/dev/Projects/atelier/src/components"),
                file("/home/dev/Projects/atelier/src/main.swift"),
            ],
            "/home/dev/Projects/atelier/src/components": [
                file("/home/dev/Projects/atelier/src/components/Button.swift"),
            ],
            "/home/dev/Projects/atelier/docs": [
                file("/home/dev/Projects/atelier/docs/plan.md"),
            ],
            // Symlinked dir: same children reachable under the lexical path.
            "/home/dev/Projects/atelier/shared": [
                file("/home/dev/Projects/atelier/shared/asset.png", size: 9000),
            ],
            "/home/dev/Projects/empty": [],
            "/home/dev/Projects/huge": (1...40).map {
                file("/home/dev/Projects/huge/file-\(String(format: "%03d", $0)).txt")
            },
            "/home/dev/.config": [
                file("/home/dev/.config/settings.toml", size: 300),
            ],
            "/home/dev/Documents": [
                file("/home/dev/Documents/taxes.pdf", size: 120_000),
            ],
        ]
    }()

    func workspaceRoots() async throws -> [RemoteWorkspaceRoot] {
        mockRoots
    }

    func listDirectory(path: String, showHidden: Bool) async throws -> DirectoryListing {
        if path.hasSuffix("/secrets") {
            throw CockpitError.api(
                code: "permission_denied", message: "permission denied: \(path)", sessionID: nil
            )
        }
        guard let children = mockTree[path] else {
            throw CockpitError.api(code: "not_found", message: "not found: \(path)", sessionID: nil)
        }
        let visible = children.filter { showHidden || !$0.name.hasPrefix(".") }
        return DirectoryListing(
            path: path,
            truncated: path.hasSuffix("/huge"),
            entries: visible
        )
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

import Foundation
import ATCAPI

extension AppModel {
    /// Preview/test fixture: an ephemeral Connection store (unique
    /// UserDefaults suite, nothing persisted to .standard) holding one
    /// Connection backed by the given client.
    static func preview(client: any ATCClient = MockATCClient()) -> AppModel {
        let defaults = UserDefaults(suiteName: "preview.appmodel.\(UUID().uuidString)")!
        let store = ConnectionsStore(defaults: defaults, credentials: InMemoryCredentialStore())
        _ = try? store.add(name: "Workstation", urlString: "http://workstation.example:7331", token: "")
        return AppModel(
            connections: store,
            clientFactory: { _ in client },
            terminalRecoveryMonitor: .disabled()
        )
    }

    /// Preview/test fixture with several Connections, each backed by its own
    /// client — for surfaces (e.g. the New Project Connection selector) that
    /// only make sense with more than one Connection.
    static func preview(connections: [(name: String, client: any ATCClient)]) -> AppModel {
        let defaults = UserDefaults(suiteName: "preview.appmodel.\(UUID().uuidString)")!
        let store = ConnectionsStore(defaults: defaults, credentials: InMemoryCredentialStore())
        var clientsByID: [UUID: any ATCClient] = [:]
        for (index, connection) in connections.enumerated() {
            // Distinct host per Connection so the store's duplicate check
            // doesn't reject the fixtures.
            if let record = try? store.add(
                name: connection.name,
                urlString: "http://connection-\(index).example:7331",
                token: ""
            ) {
                clientsByID[record.id] = connection.client
            }
        }
        return AppModel(
            connections: store,
            clientFactory: { clientsByID[$0.id] ?? MockATCClient() },
            terminalRecoveryMonitor: .disabled()
        )
    }
}

/// Canned-data client for previews and offline development.
nonisolated struct MockATCClient: ATCClient {
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
        // Active project with zero workspaces, for the Dashboard's inline
        // "New Workspace" empty row.
        Project(
            id: "prj_notes",
            name: "Notes",
            workingDir: "/home/dev/Documents",
            createdAt: Date(timeIntervalSinceNow: -250000),
            updatedAt: Date(timeIntervalSinceNow: -250000)
        ),
    ]

    /// Stable-ID workspace fixtures; one archived for filter coverage.
    var mockWorkspaces: [Workspace] = [
        Workspace(
            id: "wsp_parser",
            projectId: "prj_atelier",
            name: "Parser fixes",
            createdAt: Date(timeIntervalSinceNow: -200000),
            updatedAt: Date(timeIntervalSinceNow: -60)
        ),
        Workspace(
            id: "wsp_refactor",
            projectId: "prj_atelier",
            name: "Refactor",
            createdAt: Date(timeIntervalSinceNow: -90000),
            updatedAt: Date(timeIntervalSinceNow: -86400)
        ),
        Workspace(
            id: "wsp_blazerr",
            projectId: "prj_blazerr",
            name: "Spike",
            createdAt: Date(timeIntervalSinceNow: -150000),
            updatedAt: Date(timeIntervalSinceNow: -30)
        ),
        Workspace(
            id: "wsp_archived",
            projectId: "prj_atelier",
            name: "Old experiment",
            createdAt: Date(timeIntervalSinceNow: -400000),
            updatedAt: Date(timeIntervalSinceNow: -300000),
            archivedAt: Date(timeIntervalSinceNow: -300000)
        ),
    ]

    /// Nested refs derived from the fixtures above, for tagging sessions.
    private static let atelierRef = SessionProject(id: "prj_atelier", name: "Atelier")
    private static let blazerrRef = SessionProject(id: "prj_blazerr", name: "Blazerr")
    private static let parserRef = SessionWorkspace(id: "wsp_parser", name: "Parser fixes")
    private static let refactorRef = SessionWorkspace(id: "wsp_refactor", name: "Refactor")
    private static let blazerrWorkspaceRef = SessionWorkspace(id: "wsp_blazerr", name: "Spike")

    var mockSessions: [Session] = [
        Session(
            id: "ses_running",
            name: "Fix the parser",
            action: "claude",
            environment: "host-login-shell",
            workingDir: "/home/dev/Projects/atelier",
            status: .live,
            createdAt: Date(timeIntervalSinceNow: -3600),
            updatedAt: Date(timeIntervalSinceNow: -60),
            workspace: MockATCClient.parserRef,
            project: MockATCClient.atelierRef
        ),
        Session(
            id: "ses_starting",
            action: "codex",
            environment: "host-login-shell",
            workingDir: "/home/dev/Projects/huge",
            status: .live,
            createdAt: Date(timeIntervalSinceNow: -30),
            updatedAt: Date(timeIntervalSinceNow: -30),
            workspace: MockATCClient.blazerrWorkspaceRef,
            project: MockATCClient.blazerrRef
        ),
        Session(
            id: "ses_failed",
            action: "lazygit",
            environment: "host-login-shell",
            workingDir: "/home/dev",
            status: .ended,
            createdAt: Date(timeIntervalSinceNow: -7200),
            updatedAt: Date(timeIntervalSinceNow: -7100)
        ),
        Session(
            id: "ses_done",
            name: "Yesterday's refactor",
            action: "claude",
            environment: "host-login-shell",
            workingDir: "/home/dev/Projects/atelier",
            status: .ended,
            createdAt: Date(timeIntervalSinceNow: -90000),
            updatedAt: Date(timeIntervalSinceNow: -86400),
            workspace: MockATCClient.refactorRef,
            project: MockATCClient.atelierRef
        ),
        // Interactive Shell (nil action) → classified as a Terminal.
        Session(
            id: "ses_shell",
            action: nil,
            environment: "host-login-shell",
            workingDir: "/home/dev/Projects/atelier",
            status: .live,
            createdAt: Date(timeIntervalSinceNow: -1800),
            updatedAt: Date(timeIntervalSinceNow: -120),
            workspace: MockATCClient.parserRef,
            project: MockATCClient.atelierRef
        ),
        // General (non-agent) action → classified as a Terminal.
        Session(
            id: "ses_lazygit",
            action: "lazygit",
            environment: "host-login-shell",
            workingDir: "/home/dev/Projects/atelier",
            status: .live,
            createdAt: Date(timeIntervalSinceNow: -900),
            updatedAt: Date(timeIntervalSinceNow: -60),
            workspace: MockATCClient.parserRef,
            project: MockATCClient.atelierRef
        ),
        // Action deleted after the session ended (unresolvable) → Terminal.
        Session(
            id: "ses_ghost",
            action: "ghost",
            environment: "host-login-shell",
            workingDir: "/home/dev/Projects/atelier",
            status: .ended,
            createdAt: Date(timeIntervalSinceNow: -50000),
            updatedAt: Date(timeIntervalSinceNow: -49000),
            workspace: MockATCClient.refactorRef,
            project: MockATCClient.atelierRef
        ),
        // Another ended agent session.
        Session(
            id: "ses_archived",
            name: "Abandoned attempt",
            action: "claude",
            environment: "host-login-shell",
            workingDir: "/home/dev/Projects/atelier",
            status: .ended,
            createdAt: Date(timeIntervalSinceNow: -200000),
            updatedAt: Date(timeIntervalSinceNow: -190000),
            workspace: MockATCClient.parserRef,
            project: MockATCClient.atelierRef
        ),
    ]

    func health() async throws -> Health { Health(status: "ok") }

    func version() async throws -> Version {
        Version(name: "atc", version: "dev", commit: "unknown")
    }

    func sessions(status: SessionStatus?) async throws -> [Session] {
        mockSessions.filter { status == nil || $0.status == status }
    }

    func session(id: String) async throws -> SessionDetail {
        guard let session = mockSessions.first(where: { $0.id == id }) else {
            throw ATCError.api(code: "session_not_found", message: "session not found: \(id)", sessionID: id)
        }
        return detail(from: session)
    }

    func startSession(_ request: StartSessionRequest) async throws -> SessionDetail {
        // Mirror the server's workspace rules so previews/tests fail where
        // the real server would.
        guard !request.workspaceId.trimmingCharacters(in: .whitespaces).isEmpty else {
            throw ATCError.api(
                code: "invalid_request", message: "workspaceId is required", sessionID: nil
            )
        }
        let workspace = try lookupWorkspace(request.workspaceId)
        guard !workspace.isArchived else {
            throw ATCError.api(
                code: "workspace_archived",
                message: "workspace is archived: \(workspace.id)", sessionID: nil
            )
        }
        let project = try lookupProject(workspace.projectId)
        let session = Session(
            id: "ses_" + String(UUID().uuidString.lowercased().prefix(8)),
            name: request.name,
            action: request.action,
            environment: request.environment ?? "host-login-shell",
            workingDir: project.workingDir,
            status: .live,
            createdAt: Date(),
            updatedAt: Date(),
            workspace: SessionWorkspace(id: workspace.id, name: workspace.name),
            project: SessionProject(id: project.id, name: project.name)
        )
        return detail(from: session)
    }

    func renameSession(id: String, name: String) async throws -> SessionDetail {
        let trimmed = name.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else {
            throw ATCError.api(code: "invalid_request", message: "name is required", sessionID: nil)
        }
        var detail = try await session(id: id)
        detail.name = trimmed
        detail.updatedAt = Date()
        return detail
    }

    func deleteSession(id: String) async throws {
        _ = try await self.session(id: id)
    }

    func sendText(sessionID: String, text: String) async throws {}
    func sendKey(sessionID: String, key: String) async throws {}

    /// Shared across copies of this value type so action mutations survive
    /// refreshes — the settings UI is interactive in previews.
    let actionRegistry = MockActionRegistry()

    func actions() async throws -> [ATCAction] {
        actionRegistry.list()
    }

    func action(name: String) async throws -> ATCAction {
        try actionRegistry.detail(name: name)
    }

    func createAction(_ request: ActionWriteRequest) async throws -> ATCAction {
        try actionRegistry.create(request)
    }

    func updateAction(name: String, _ request: ActionWriteRequest) async throws -> ATCAction {
        try actionRegistry.update(name: name, request)
    }

    func setActionEnabled(name: String, enabled: Bool) async throws -> ATCAction {
        try actionRegistry.setEnabled(name: name, enabled: enabled)
    }

    func deleteAction(name: String) async throws {
        try actionRegistry.delete(name: name)
    }

    func environments() async throws -> [ATCEnvironment] {
        let json = Data(#"""
        {"environments":[{"name":"host-login-shell","kind":"host-login-shell","label":"Host login shell","default":true}]}
        """#.utf8)
        struct Envelope: Decodable { var environments: [ATCEnvironment] }
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
        // Unique id per call: repeated creates in a preview must not
        // produce List rows with colliding identities. (The mock is a
        // value type, so the created project still vanishes on the next
        // refresh — same fidelity limit as mock session starts.)
        Project(
            id: "prj_" + String(UUID().uuidString.lowercased().prefix(8)),
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
        status: SessionStatus?
    ) async throws -> [Session] {
        _ = try lookupProject(projectID)
        return mockSessions.filter { session in
            session.project?.id == projectID
                && (status == nil || session.status == status)
        }
    }

    func deleteProject(id: String) async throws {
        let project = try lookupProject(id)
        guard !mockWorkspaces.contains(where: { $0.projectId == project.id }) else {
            throw ATCError.api(
                code: "project_has_workspaces",
                message: "project has workspaces: \(id)", sessionID: nil
            )
        }
    }

    // MARK: - Workspaces

    func workspaces(projectID: String?, includeArchived: Bool) async throws -> [Workspace] {
        mockWorkspaces.filter { workspace in
            (projectID == nil || workspace.projectId == projectID)
                && (includeArchived || !workspace.isArchived)
        }
    }

    func workspace(id: String) async throws -> Workspace {
        try lookupWorkspace(id)
    }

    func createWorkspace(projectID: String, name: String) async throws -> Workspace {
        let project = try lookupProject(projectID)
        guard !project.isArchived else {
            throw ATCError.api(
                code: "project_archived", message: "project is archived: \(projectID)", sessionID: nil
            )
        }
        // Unique id per call, same fidelity limit as mock project creates:
        // the value type means it vanishes on the next refresh.
        return Workspace(
            id: "wsp_" + String(UUID().uuidString.lowercased().prefix(8)),
            projectId: projectID,
            name: name,
            createdAt: Date(),
            updatedAt: Date()
        )
    }

    func renameWorkspace(id: String, name: String) async throws -> Workspace {
        var workspace = try lookupWorkspace(id)
        workspace.name = name
        workspace.updatedAt = Date()
        return workspace
    }

    func archiveWorkspace(id: String) async throws -> Workspace {
        var workspace = try lookupWorkspace(id)
        workspace.archivedAt = Date()
        workspace.updatedAt = Date()
        return workspace
    }

    func unarchiveWorkspace(id: String) async throws -> Workspace {
        var workspace = try lookupWorkspace(id)
        workspace.archivedAt = nil
        workspace.updatedAt = Date()
        return workspace
    }

    func deleteWorkspace(id: String) async throws {
        _ = try lookupWorkspace(id)
    }

    func workspaceSessions(
        workspaceID: String,
        status: SessionStatus?
    ) async throws -> [Session] {
        _ = try lookupWorkspace(workspaceID)
        return mockSessions.filter { session in
            session.workspace?.id == workspaceID
                && (status == nil || session.status == status)
        }
    }

    private func lookupWorkspace(_ id: String) throws -> Workspace {
        guard let workspace = mockWorkspaces.first(where: { $0.id == id }) else {
            throw ATCError.api(
                code: "workspace_not_found", message: "workspace not found: \(id)", sessionID: nil
            )
        }
        return workspace
    }

    private func lookupProject(_ id: String) throws -> Project {
        guard let project = mockProjects.first(where: { $0.id == id }) else {
            throw ATCError.api(
                code: "project_not_found", message: "project not found: \(id)", sessionID: nil
            )
        }
        return project
    }

    // MARK: - File system

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
            "/": [
                dir("/home"),
            ],
            "/home": [
                dir("/home/dev"),
            ],
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

    func listDirectory(path: String, showHidden: Bool) async throws -> DirectoryListing {
        let resolvedPath = path.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? "/home/dev" : path
        if resolvedPath.hasSuffix("/secrets") {
            throw ATCError.api(
                code: "permission_denied", message: "permission denied: \(resolvedPath)", sessionID: nil
            )
        }
        guard let children = mockTree[resolvedPath] else {
            throw ATCError.api(code: "not_found", message: "not found: \(resolvedPath)", sessionID: nil)
        }
        let visible = children.filter { showHidden || !$0.name.hasPrefix(".") }
        return DirectoryListing(
            path: resolvedPath,
            truncated: resolvedPath.hasSuffix("/huge"),
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

/// In-memory mirror of the server's action registry: built-in defaults
/// (claude, codex) always present underneath a file overlay of custom
/// actions and built-in overrides. Mimics the server's origin computation,
/// validation, and error codes so previews/tests fail where it would.
nonisolated final class MockActionRegistry: @unchecked Sendable {
    private let lock = NSLock()

    /// Built-in definitions (origin field is ignored; computed at read time).
    /// Codex carries synthetic params so param-rendering UI has a fixture.
    private let builtins: [String: ATCAction] = [
        "claude": ATCAction(
            name: "claude", type: "agent", origin: "builtin", enabled: true,
            label: "Claude", description: "Claude Code CLI",
            command: "claude", args: [], prompt: .init()
        ),
        "codex": ATCAction(
            name: "codex", type: "agent", origin: "builtin", enabled: true,
            label: "Codex", description: "OpenAI Codex CLI",
            command: "codex", args: [], prompt: .init(),
            params: [
                "model": .init(type: "enum", values: ["fast", "smart"], default: .string("fast"), flag: "--model", label: "Model"),
                "verbose": .init(type: "bool", flag: "--verbose", label: "Verbose"),
            ]
        ),
    ]

    /// The overlay: custom actions plus built-in overrides.
    private var fileEntries: [String: ATCAction] = [
        "lazygit": ATCAction(
            name: "lazygit", origin: "custom", enabled: true,
            label: "LazyGit", description: "Open LazyGit",
            command: "lazygit", args: []
        ),
    ]

    /// List view: origin computed, `command`/`args` stripped like the server.
    func list() -> [ATCAction] {
        lock.withLock {
            allNames().compactMap { name in
                guard var action = effective(name: name) else { return nil }
                action.command = nil
                action.args = nil
                return action
            }
        }
    }

    func detail(name: String) throws -> ATCAction {
        try lock.withLock { try requireEffective(name: name) }
    }

    func create(_ request: ActionWriteRequest) throws -> ATCAction {
        try lock.withLock {
            let name = try resolveName(request)
            try validate(request)
            guard effective(name: name) == nil else {
                throw ATCError.api(
                    code: "action_conflict", message: "action already exists: \(name)", sessionID: nil
                )
            }
            fileEntries[name] = definition(from: request, name: name)
            return try requireEffective(name: name)
        }
    }

    func update(name: String, _ request: ActionWriteRequest) throws -> ATCAction {
        try lock.withLock {
            if let bodyName = request.name, bodyName != name {
                throw ATCError.api(
                    code: "invalid_request", message: "body name must match route name", sessionID: nil
                )
            }
            try validate(request)
            store(definition(from: request, name: name), name: name)
            return try requireEffective(name: name)
        }
    }

    func setEnabled(name: String, enabled: Bool) throws -> ATCAction {
        try lock.withLock {
            var action = try requireEffective(name: name)
            action.enabled = enabled
            store(action, name: name)
            return try requireEffective(name: name)
        }
    }

    func delete(name: String) throws {
        try lock.withLock {
            if fileEntries.removeValue(forKey: name) != nil { return }
            if builtins[name] != nil {
                throw ATCError.api(
                    code: "action_conflict",
                    message: "built-in action cannot be removed: \(name)", sessionID: nil
                )
            }
            throw ATCError.api(
                code: "action_not_found", message: "action not found: \(name)", sessionID: nil
            )
        }
    }

    // MARK: - Server-rule internals (call only with the lock held)

    private func allNames() -> [String] {
        Set(builtins.keys).union(fileEntries.keys).sorted()
    }

    private func effective(name: String) -> ATCAction? {
        if var entry = fileEntries[name] {
            entry.name = name
            entry.origin = origin(of: entry, name: name)
            return entry
        }
        if var builtin = builtins[name] {
            builtin.origin = "builtin"
            return builtin
        }
        return nil
    }

    private func requireEffective(name: String) throws -> ATCAction {
        guard let action = effective(name: name) else {
            throw ATCError.api(
                code: "action_not_found", message: "action not found: \(name)", sessionID: nil
            )
        }
        return action
    }

    /// An overlay that only flips `enabled` still reports `builtin`
    /// (server: sameExceptDisabled).
    private func origin(of entry: ATCAction, name: String) -> String {
        guard let builtin = builtins[name] else { return "custom" }
        return sameExceptEnabled(entry, builtin) ? "builtin" : "modified"
    }

    private func sameExceptEnabled(_ a: ATCAction, _ b: ATCAction) -> Bool {
        a.label == b.label && a.description == b.description
            && a.command == b.command && a.args == b.args
            && a.prompt == b.prompt && a.params == b.params
    }

    /// Writes an entry to the overlay, dropping it when it exactly matches
    /// the built-in default (the server prunes no-op overrides).
    private func store(_ action: ATCAction, name: String) {
        if let builtin = builtins[name],
           sameExceptEnabled(action, builtin), action.enabled == builtin.enabled {
            fileEntries[name] = nil
        } else {
            fileEntries[name] = action
        }
    }

    private func resolveName(_ request: ActionWriteRequest) throws -> String {
        let name = request.name ?? ActionName.slugify(request.label ?? "")
        guard !name.isEmpty else {
            throw ATCError.api(
                code: "invalid_request", message: "name or label is required", sessionID: nil
            )
        }
        guard ActionName.isValid(name) else {
            throw ATCError.api(
                code: "invalid_action",
                message: "action name \"\(name)\" must match ^[A-Za-z0-9_-]+$", sessionID: nil
            )
        }
        return name
    }

    private func validate(_ request: ActionWriteRequest) throws {
        guard !request.command.trimmingCharacters(in: .whitespaces).isEmpty else {
            throw ATCError.api(
                code: "invalid_action", message: "action command is required", sessionID: nil
            )
        }
        for (paramName, spec) in request.params ?? [:] {
            guard spec.isEnum || spec.isBool else {
                throw ATCError.api(
                    code: "invalid_action",
                    message: "param \(paramName): unsupported type \(spec.type)", sessionID: nil
                )
            }
            if spec.isEnum, (spec.values ?? []).isEmpty {
                throw ATCError.api(
                    code: "invalid_action",
                    message: "param \(paramName): enum values are required", sessionID: nil
                )
            }
        }
    }

    private func definition(from request: ActionWriteRequest, name: String) -> ATCAction {
        ATCAction(
            name: name,
            origin: "custom",
            enabled: request.enabled ?? true,
            label: request.label,
            description: request.description,
            command: request.command,
            args: request.args ?? [],
            prompt: request.prompt,
            params: request.params ?? [:]
        )
    }
}

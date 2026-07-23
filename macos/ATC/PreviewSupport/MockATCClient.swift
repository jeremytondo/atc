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
    /// project-scoped session lands in a browsable directory.
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
            updatedAt: Date(timeIntervalSinceNow: -200000)
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

    /// Stable-ID workspace fixtures.
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
            id: "wsp_experiment",
            projectId: "prj_atelier",
            name: "Old experiment",
            createdAt: Date(timeIntervalSinceNow: -400000),
            updatedAt: Date(timeIntervalSinceNow: -300000)
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
            actionId: "act_vpj2tlg9viqd8ms52ptuvao5c4",
            actionName: "Claude",
            isAgent: true,
            workingDir: "/home/dev/Projects/atelier",
            status: .live,
            createdAt: Date(timeIntervalSinceNow: -3600),
            updatedAt: Date(timeIntervalSinceNow: -60),
            workspace: MockATCClient.parserRef,
            project: MockATCClient.atelierRef
        ),
        Session(
            id: "ses_starting",
            actionId: "act_fh9g7e6571qo53r0t647ughtfg",
            actionName: "Codex",
            isAgent: true,
            workingDir: "/home/dev/Projects/huge",
            status: .live,
            createdAt: Date(timeIntervalSinceNow: -30),
            updatedAt: Date(timeIntervalSinceNow: -30),
            workspace: MockATCClient.blazerrWorkspaceRef,
            project: MockATCClient.blazerrRef
        ),
        Session(
            id: "ses_failed",
            actionId: "act_00000000000000000000000000",
            actionName: "LazyGit",
            isAgent: false,
            workingDir: "/home/dev",
            status: .ended,
            createdAt: Date(timeIntervalSinceNow: -7200),
            updatedAt: Date(timeIntervalSinceNow: -7100)
        ),
        Session(
            id: "ses_done",
            name: "Yesterday's refactor",
            actionId: "act_vpj2tlg9viqd8ms52ptuvao5c4",
            actionName: "Claude",
            isAgent: true,
            workingDir: "/home/dev/Projects/atelier",
            status: .ended,
            createdAt: Date(timeIntervalSinceNow: -90000),
            updatedAt: Date(timeIntervalSinceNow: -86400),
            workspace: MockATCClient.refactorRef,
            project: MockATCClient.atelierRef
        ),
        // Interactive Shell (nil action ID) → classified as a Terminal.
        Session(
            id: "ses_shell",
            isAgent: false,
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
            actionId: "act_00000000000000000000000000",
            actionName: "LazyGit",
            isAgent: false,
            workingDir: "/home/dev/Projects/atelier",
            status: .live,
            createdAt: Date(timeIntervalSinceNow: -900),
            updatedAt: Date(timeIntervalSinceNow: -60),
            workspace: MockATCClient.parserRef,
            project: MockATCClient.atelierRef
        ),
        // Action deleted after the session ended; copied identity remains.
        Session(
            id: "ses_ghost",
            actionId: "act_11111111111111111111111111",
            actionName: "Deleted Tool",
            isAgent: false,
            workingDir: "/home/dev/Projects/atelier",
            status: .ended,
            createdAt: Date(timeIntervalSinceNow: -50000),
            updatedAt: Date(timeIntervalSinceNow: -49000),
            workspace: MockATCClient.refactorRef,
            project: MockATCClient.atelierRef
        ),
        // Another ended agent session.
        Session(
            id: "ses_abandoned",
            name: "Abandoned attempt",
            actionId: "act_vpj2tlg9viqd8ms52ptuvao5c4",
            actionName: "Claude",
            isAgent: true,
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

    func session(id: String) async throws -> Session {
        guard let session = mockSessions.first(where: { $0.id == id }) else {
            throw ATCError.api(code: "session_not_found", message: "session not found: \(id)", sessionID: id)
        }
        return session
    }

    func startSession(_ request: StartSessionRequest) async throws -> Session {
        // Mirror the server's workspace validation so previews and tests fail
        // where the real server would.
        guard !request.workspaceId.trimmingCharacters(in: .whitespaces).isEmpty else {
            throw ATCError.api(
                code: "invalid_request", message: "workspaceId is required", sessionID: nil
            )
        }
        let workspace = try lookupWorkspace(request.workspaceId)
        let project = try lookupProject(workspace.projectId)
        let action: ATCAction?
        if let actionID = request.actionId {
            let resolved = try actionsState.detail(id: actionID)
            guard resolved.enabled else {
                throw ATCError.api(
                    code: "action_disabled",
                    message: "action is disabled: \(actionID)",
                    sessionID: nil
                )
            }
            action = resolved
        } else {
            action = nil
        }
        let session = Session(
            id: "ses_" + String(UUID().uuidString.lowercased().prefix(8)),
            name: request.name,
            actionId: action?.id,
            actionName: action?.name,
            isAgent: action?.isAgent ?? false,
            workingDir: project.workingDir,
            status: .live,
            createdAt: Date(),
            updatedAt: Date(),
            workspace: SessionWorkspace(id: workspace.id, name: workspace.name),
            project: SessionProject(id: project.id, name: project.name)
        )
        return session
    }

    func renameSession(id: String, name: String) async throws -> Session {
        let trimmed = name.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else {
            throw ATCError.api(code: "invalid_request", message: "name is required", sessionID: nil)
        }
        var session = try await session(id: id)
        session.name = trimmed
        session.updatedAt = Date()
        return session
    }

    func deleteSession(id: String) async throws {
        _ = try await self.session(id: id)
    }

    func sendText(sessionID: String, text: String) async throws {}
    func sendKey(sessionID: String, key: String) async throws {}

    /// Shared across copies of this value type so action mutations survive
    /// refreshes — the settings UI is interactive in previews.
    let actionsState = MockActionsState()

    func actions() async throws -> [ATCAction] {
        actionsState.list()
    }

    func action(id: String) async throws -> ATCAction {
        try actionsState.detail(id: id)
    }

    func createAction(_ request: ActionCreate) async throws -> ATCAction {
        try actionsState.create(request)
    }

    func updateAction(id: String, _ request: ActionPatch) async throws -> ATCAction {
        try actionsState.update(id: id, request)
    }

    func deleteAction(id: String) async throws {
        try actionsState.delete(id: id)
    }

    // MARK: - Projects

    func projects() async throws -> [Project] {
        mockProjects
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

    func workspaces(projectID: String?) async throws -> [Workspace] {
        mockWorkspaces.filter { workspace in
            (projectID == nil || workspace.projectId == projectID)
        }
    }

    func workspace(id: String) async throws -> Workspace {
        try lookupWorkspace(id)
    }

    func createWorkspace(projectID: String, name: String) async throws -> Workspace {
        _ = try lookupProject(projectID)
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

}

/// Thread-safe in-memory mirror of the server's SQLite Action CRUD.
nonisolated final class MockActionsState: @unchecked Sendable {
    private let lock = NSLock()

    private var entries: [String: ATCAction] = [
        "act_vpj2tlg9viqd8ms52ptuvao5c4": ATCAction(
            id: "act_vpj2tlg9viqd8ms52ptuvao5c4",
            name: "Claude",
            description: "Anthropic's coding agent",
            enabled: true,
            command: "claude",
            args: [],
            isAgent: true
        ),
        "act_fh9g7e6571qo53r0t647ughtfg": ATCAction(
            id: "act_fh9g7e6571qo53r0t647ughtfg",
            name: "Codex",
            description: "OpenAI's coding agent",
            enabled: true,
            command: "codex",
            args: [],
            isAgent: true
        ),
        "act_00000000000000000000000000": ATCAction(
            id: "act_00000000000000000000000000",
            name: "LazyGit",
            description: "Open LazyGit",
            enabled: true,
            command: "lazygit",
            args: [],
            isAgent: false
        ),
    ]

    func list() -> [ATCAction] {
        lock.withLock {
            entries.values.sorted {
                let order = $0.name.localizedCaseInsensitiveCompare($1.name)
                return order == .orderedSame ? $0.id < $1.id : order == .orderedAscending
            }
        }
    }

    func detail(id: String) throws -> ATCAction {
        try lock.withLock { try require(id: id) }
    }

    func create(_ request: ActionCreate) throws -> ATCAction {
        try lock.withLock {
            try validate(name: request.name, command: request.command)
            let suffix = UUID().uuidString
                .replacingOccurrences(of: "-", with: "")
                .lowercased()
                .prefix(26)
            let id = "act_" + String(suffix)
            let action = ATCAction(
                id: id,
                name: request.name,
                description: request.description,
                enabled: request.enabled ?? true,
                command: request.command,
                args: request.args ?? [],
                isAgent: request.isAgent ?? false
            )
            entries[action.id] = action
            return action
        }
    }

    func update(id: String, _ patch: ActionPatch) throws -> ATCAction {
        try lock.withLock {
            var action = try require(id: id)
            if let name = patch.name { action.name = name }
            if patch.clearDescription {
                action.description = nil
            } else if let description = patch.description {
                action.description = description
            }
            if let command = patch.command { action.command = command }
            if let args = patch.args { action.args = args }
            if let enabled = patch.enabled { action.enabled = enabled }
            if let isAgent = patch.isAgent { action.isAgent = isAgent }
            try validate(name: action.name, command: action.command)
            entries[id] = action
            return action
        }
    }

    func delete(id: String) throws {
        try lock.withLock {
            guard entries.removeValue(forKey: id) != nil else {
                throw ATCError.api(
                    code: "action_not_found",
                    message: "action not found: \(id)",
                    sessionID: nil
                )
            }
        }
    }

    private func require(id: String) throws -> ATCAction {
        guard let action = entries[id] else {
            throw ATCError.api(
                code: "action_not_found",
                message: "action not found: \(id)",
                sessionID: nil
            )
        }
        return action
    }

    private func validate(name: String, command: String) throws {
        guard !name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else {
            throw ATCError.api(
                code: "invalid_action",
                message: "action name is required",
                sessionID: nil
            )
        }
        guard !command.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else {
            throw ATCError.api(
                code: "invalid_action",
                message: "action command is required",
                sessionID: nil
            )
        }
    }
}

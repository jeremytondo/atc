import Foundation
import Testing
import ATCAPI
@testable import ATC

/// A client whose calls can be failed on demand and/or delayed — drives
/// reachability transitions and edit-during-refresh races.
private nonisolated final class ScriptableClient: ATCClient, @unchecked Sendable {
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
    func sessions(includeArchived: Bool, status: SessionStatus?) async throws -> [Session] {
        try await gate()
        return try await inner.sessions(includeArchived: includeArchived, status: status)
    }
    func session(id: String) async throws -> SessionDetail {
        try await gate(); return try await inner.session(id: id)
    }
    func startSession(_ request: StartSessionRequest) async throws -> SessionDetail {
        try await gate(); return try await inner.startSession(request)
    }
    func terminateSession(id: String) async throws -> SessionDetail {
        try await gate(); return try await inner.terminateSession(id: id)
    }
    func archiveSession(id: String) async throws -> SessionDetail {
        try await gate(); return try await inner.archiveSession(id: id)
    }
    func unarchiveSession(id: String) async throws -> SessionDetail {
        try await gate(); return try await inner.unarchiveSession(id: id)
    }
    func deleteSession(id: String) async throws {
        try await gate(); try await inner.deleteSession(id: id)
    }
    func sendText(sessionID: String, text: String) async throws { try await gate() }
    func sendKey(sessionID: String, key: String) async throws { try await gate() }
    func actions() async throws -> [ATCAction] { try await gate(); return try await inner.actions() }
    func action(name: String) async throws -> ATCAction {
        try await gate(); return try await inner.action(name: name)
    }
    func createAction(_ request: ActionWriteRequest) async throws -> ATCAction {
        try await gate(); return try await inner.createAction(request)
    }
    func updateAction(name: String, _ request: ActionWriteRequest) async throws -> ATCAction {
        try await gate(); return try await inner.updateAction(name: name, request)
    }
    func setActionEnabled(name: String, enabled: Bool) async throws -> ATCAction {
        try await gate(); return try await inner.setActionEnabled(name: name, enabled: enabled)
    }
    func deleteAction(name: String) async throws {
        try await gate(); try await inner.deleteAction(name: name)
    }
    func environments() async throws -> [ATCEnvironment] {
        try await gate(); return try await inner.environments()
    }
    func listDirectory(path: String, showHidden: Bool) async throws -> DirectoryListing {
        try await gate(); return try await inner.listDirectory(path: path, showHidden: showHidden)
    }
    func projects(includeArchived: Bool) async throws -> [Project] {
        try await gate(); return try await inner.projects(includeArchived: includeArchived)
    }
    func project(id: String) async throws -> Project { try await gate(); return try await inner.project(id: id) }
    func createProject(name: String, workingDir: String) async throws -> Project {
        try await gate(); return try await inner.createProject(name: name, workingDir: workingDir)
    }
    func renameProject(id: String, name: String) async throws -> Project {
        try await gate(); return try await inner.renameProject(id: id, name: name)
    }
    func archiveProject(id: String) async throws -> Project {
        try await gate(); return try await inner.archiveProject(id: id)
    }
    func unarchiveProject(id: String) async throws -> Project {
        try await gate(); return try await inner.unarchiveProject(id: id)
    }
    func projectSessions(projectID: String, includeArchived: Bool, status: SessionStatus?) async throws -> [Session] {
        try await gate()
        return try await inner.projectSessions(
            projectID: projectID, includeArchived: includeArchived, status: status
        )
    }
    func deleteProject(id: String) async throws {
        try await gate(); try await inner.deleteProject(id: id)
    }
    func workspaces(projectID: String?, includeArchived: Bool) async throws -> [Workspace] {
        try await gate()
        return try await inner.workspaces(projectID: projectID, includeArchived: includeArchived)
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
    func archiveWorkspace(id: String) async throws -> Workspace {
        try await gate(); return try await inner.archiveWorkspace(id: id)
    }
    func unarchiveWorkspace(id: String) async throws -> Workspace {
        try await gate(); return try await inner.unarchiveWorkspace(id: id)
    }
    func deleteWorkspace(id: String) async throws {
        try await gate(); try await inner.deleteWorkspace(id: id)
    }
    func workspaceSessions(workspaceID: String, includeArchived: Bool, status: SessionStatus?) async throws -> [Session] {
        try await gate()
        return try await inner.workspaceSessions(
            workspaceID: workspaceID, includeArchived: includeArchived, status: status
        )
    }
    func attachURL(sessionID: String) -> URL { inner.attachURL(sessionID: sessionID) }
    func attachHeaders() -> [String: String] { inner.attachHeaders() }
}

/// Runtime lifecycle: add/edit/delete touch only the affected runtime,
/// selection cleanup, reachability transitions, stale-result harmlessness.
@MainActor
@Suite("AppModel runtimes")
struct AppModelRuntimeTests {
    /// AppModel over an ephemeral store with one ScriptableClient per added
    /// Connection, so tests can steer each Connection independently.
    private func makeModel() -> (AppModel, () -> ScriptableClient) {
        let suite = "AppModelRuntimeTests.\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        defaults.removePersistentDomain(forName: suite)
        let store = ConnectionsStore(defaults: defaults, credentials: InMemoryCredentialStore())
        var clients: [ScriptableClient] = []
        let model = AppModel(connections: store, clientFactory: { _ in
            let client = ScriptableClient()
            clients.append(client)
            return client
        })
        return (model, { clients.last! })
    }

    @Test("adding a connection creates exactly one polling runtime")
    func addCreatesRuntime() throws {
        let (model, _) = makeModel()
        #expect(model.runtimes.isEmpty)
        let record = try model.addConnection(name: "A", urlString: "http://a:1", token: "")
        #expect(model.runtimes.count == 1)
        #expect(model.runtimes[0].id == record.id)
        #expect(model.reachability(of: record.id) == .unknown)
    }

    @Test("name-only edit keeps the same runtime and store instances")
    func nameOnlyEditKeepsRuntime() throws {
        let (model, _) = makeModel()
        let record = try model.addConnection(name: "A", urlString: "http://a:1", token: "")
        let runtime = model.runtimes[0]
        let projects = runtime.projects
        try model.updateConnection(id: record.id, name: "Renamed", urlString: "http://a:1", token: "")
        #expect(model.runtimes[0] === runtime)
        #expect(model.runtimes[0].projects === projects)
        #expect(model.runtimes[0].record.name == "Renamed")
    }

    @Test("URL change rebuilds only the affected runtime")
    func urlChangeRebuildsOne() throws {
        let (model, _) = makeModel()
        let a = try model.addConnection(name: "A", urlString: "http://a:1", token: "")
        let b = try model.addConnection(name: "B", urlString: "http://b:1", token: "")
        let runtimeA = model.runtime(id: a.id)!
        let runtimeB = model.runtime(id: b.id)!
        try model.updateConnection(id: a.id, name: "A", urlString: "http://a:2", token: "")
        #expect(model.runtime(id: a.id) !== runtimeA)
        #expect(model.runtime(id: a.id)?.record.urlString == "http://a:2")
        #expect(model.runtime(id: b.id) === runtimeB)
        // Position is preserved: creation order unchanged.
        #expect(model.runtimes.map(\.id) == [a.id, b.id])
    }

    @Test("token change also rebuilds; wouldRebuildConnection reports it")
    func tokenChangeRebuilds() throws {
        let (model, _) = makeModel()
        let a = try model.addConnection(name: "A", urlString: "http://a:1", token: "")
        #expect(model.wouldRebuildConnection(id: a.id, urlString: "http://a:1", token: "secret"))
        #expect(!model.wouldRebuildConnection(id: a.id, urlString: "http://a:1", token: ""))
        // Unsaved-format input normalizes before comparison ("a:1" == "http://a:1").
        #expect(!model.wouldRebuildConnection(id: a.id, urlString: "a:1", token: ""))
        let runtime = model.runtime(id: a.id)!
        try model.updateConnection(id: a.id, name: "A", urlString: "http://a:1", token: "secret")
        #expect(model.runtime(id: a.id) !== runtime)
    }

    @Test("deleting a connection removes its runtime and clears its selection")
    func deleteClearsOwnSelection() throws {
        let (model, _) = makeModel()
        let a = try model.addConnection(name: "A", urlString: "http://a:1", token: "")
        let b = try model.addConnection(name: "B", urlString: "http://b:1", token: "")
        model.selection = SessionRef(connectionID: a.id, sessionID: "ses_running")
        model.removeConnection(id: a.id)
        #expect(model.runtimes.map(\.id) == [b.id])
        #expect(model.selection == nil)
    }

    @Test("deleting a connection keeps a selection on another connection")
    func deleteKeepsForeignSelection() throws {
        let (model, _) = makeModel()
        let a = try model.addConnection(name: "A", urlString: "http://a:1", token: "")
        let b = try model.addConnection(name: "B", urlString: "http://b:1", token: "")
        let kept = SessionRef(connectionID: b.id, sessionID: "ses_running")
        model.selection = kept
        model.removeConnection(id: a.id)
        #expect(model.selection == kept)
    }

    @Test("reachability: unknown → connected → unreachable → connected")
    func reachabilityTransitions() async throws {
        let (model, lastClient) = makeModel()
        let a = try model.addConnection(name: "A", urlString: "http://a:1", token: "")
        let client = lastClient()
        let runtime = model.runtime(id: a.id)!
        // Drive refreshes manually — a concurrent poll refresh could bump
        // the stores' generation and drop this test's results.
        runtime.stopPolling()
        #expect(runtime.reachability == .unknown || runtime.reachability == .connected)

        await runtime.refresh()
        #expect(runtime.reachability == .connected)

        client.shouldFail = true
        await runtime.refresh()
        #expect(runtime.reachability == .unreachable)
        // Loaded data survives a red connection (stores keep last success).
        #expect(!runtime.projects.projects.isEmpty)

        client.shouldFail = false
        await runtime.refresh()
        #expect(runtime.reachability == .connected)
    }

    @Test("session lookup resolves through the owning runtime")
    func sessionLookup() async throws {
        let (model, _) = makeModel()
        let a = try model.addConnection(name: "A", urlString: "http://a:1", token: "")
        // Stop the poll task first: its concurrent refresh could bump the
        // stores' generation and drop this manual refresh's result.
        model.runtime(id: a.id)!.stopPolling()
        await model.runtime(id: a.id)!.refresh()
        let ref = SessionRef(connectionID: a.id, sessionID: "ses_running")
        #expect(model.session(for: ref)?.id == "ses_running")
        #expect(model.session(for: SessionRef(connectionID: UUID(), sessionID: "ses_running")) == nil)
    }

    @Test("a refresh in flight when its connection is edited lands harmlessly")
    func staleRefreshHarmless() async throws {
        let (model, lastClient) = makeModel()
        let a = try model.addConnection(name: "A", urlString: "http://a:1", token: "")
        let slowClient = lastClient()
        slowClient.delay = .milliseconds(150)
        let oldRuntime = model.runtime(id: a.id)!
        let slowRefresh = Task { await oldRuntime.refresh() }

        // Rebuild while the old refresh is still sleeping in the client.
        try model.updateConnection(id: a.id, name: "A", urlString: "http://a:2", token: "")
        let newRuntime = model.runtime(id: a.id)!
        #expect(newRuntime !== oldRuntime)

        await slowRefresh.value
        // The late result mutated only the discarded runtime's stores; the
        // replacement runtime is untouched by it.
        #expect(model.runtime(id: a.id) === newRuntime)
        #expect(model.runtimes.count == 1)
    }

    @Test("includeArchived propagates to every runtime's stores, including rebuilt ones")
    func includeArchivedPropagates() throws {
        let (model, _) = makeModel()
        let a = try model.addConnection(name: "A", urlString: "http://a:1", token: "")
        try model.addConnection(name: "B", urlString: "http://b:1", token: "")
        model.includeArchived = true
        for runtime in model.runtimes {
            #expect(runtime.projects.includeArchived)
            #expect(runtime.sessions.includeArchived)
        }
        // A rebuilt runtime inherits the current filter.
        try model.updateConnection(id: a.id, name: "A", urlString: "http://a:9", token: "")
        #expect(model.runtime(id: a.id)!.projects.includeArchived)
    }
}

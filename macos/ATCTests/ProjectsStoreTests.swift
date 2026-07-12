import Foundation
import Testing
import ATCAPI
@testable import ATC

/// ProjectsStore behavior: refresh, archived filtering, and
/// merge-after-mutation semantics.
///
/// Mutation tests use `StatefulProjectsClient` instead of the value-type
/// `MockATCClient`: the store fires an unawaited refresh after every
/// mutation, and only a client whose state actually changed keeps those
/// refreshes from resurrecting pre-mutation fixtures mid-test.
@Suite("ProjectsStore")
struct ProjectsStoreTests {
    @Test("refresh always loads archived projects too; views filter locally")
    func refreshIncludesArchived() async throws {
        let store = ProjectsStore(client: MockATCClient())
        await store.refresh()
        #expect(store.hasLoadedOnce)
        #expect(store.lastError == nil)
        #expect(store.projects.count == 4)
        #expect(store.projects.contains { $0.id == "prj_scratch" && $0.isArchived })
    }

    @Test("create merges the new project at the front with a unique id")
    func createMerges() async throws {
        let store = ProjectsStore(client: StatefulProjectsClient())
        await store.refresh()
        let first = try await store.create(name: "Fresh", workingDir: "/home/dev/fresh")
        let second = try await store.create(name: "Fresher", workingDir: "/home/dev/fresher")
        #expect(first.id != second.id)
        #expect(store.projects.first?.id == second.id)
        #expect(store.projects.contains { $0.id == first.id })
    }

    @Test("archive keeps the project in the list; views hide it locally")
    func archiveKeepsRow() async throws {
        let store = ProjectsStore(client: StatefulProjectsClient())
        await store.refresh()
        let target = try #require(store.projects.first)
        try await store.archive(id: target.id)
        #expect(store.project(id: target.id)?.isArchived == true)
    }

    @Test("delete removes the project row locally")
    func deleteRemoves() async throws {
        let store = ProjectsStore(client: StatefulProjectsClient())
        await store.refresh()
        let target = try #require(store.projects.first)
        try await store.delete(id: target.id)
        #expect(!store.projects.contains { $0.id == target.id })
    }

    @Test("rename updates the project in place")
    func renameUpdates() async throws {
        let store = ProjectsStore(client: StatefulProjectsClient())
        await store.refresh()
        let target = try #require(store.projects.first)
        try await store.rename(id: target.id, name: "Renamed")
        #expect(store.project(id: target.id)?.name == "Renamed")
    }

    @Test("failed refresh keeps the error and empty list")
    func refreshError() async throws {
        let store = ProjectsStore(client: StatefulProjectsClient(failAll: true))
        await store.refresh()
        #expect(store.lastError != nil)
        #expect(store.projects.isEmpty)
    }

    @Test("an older refresh finishing late does not settle the newer one's loading state")
    func staleRefreshKeepsLoading() async throws {
        let client = GatedProjectsClient()
        let store = ProjectsStore(client: client)

        // Start refresh #1 and wait until it is parked inside the client,
        // so refresh #2 is guaranteed the newer generation.
        let first = Task { await store.refresh() }
        try await client.waitForWaiters(1)
        let second = Task { await store.refresh() }
        try await client.waitForWaiters(2)

        // The older request completes while the newer is still in flight:
        // the spinner must stay on and hasLoadedOnce must stay false.
        client.releaseNext()
        await first.value
        #expect(store.isLoading)
        #expect(!store.hasLoadedOnce)

        client.releaseNext()
        await second.value
        #expect(!store.isLoading)
        #expect(store.hasLoadedOnce)
    }
}

/// Client whose `projects()` parks until the test releases it, for
/// deterministic overlapping-refresh sequencing. Waiters release in call
/// order.
final class GatedProjectsClient: ATCClient, @unchecked Sendable {
    private let lock = NSLock()
    private var waiters: [CheckedContinuation<Void, Never>] = []

    func waitForWaiters(_ count: Int) async throws {
        for _ in 0..<500 {
            if lock.withLock({ waiters.count }) >= count { return }
            try await Task.sleep(for: .milliseconds(5))
        }
        throw ATCError.badStatus(500)
    }

    func releaseNext() {
        let waiter = lock.withLock { waiters.isEmpty ? nil : waiters.removeFirst() }
        waiter?.resume()
    }

    func projects(includeArchived: Bool) async throws -> [Project] {
        await withCheckedContinuation { continuation in
            lock.withLock { waiters.append(continuation) }
        }
        return []
    }

    // MARK: - Unused surface

    func health() async throws -> Health { throw ATCError.badStatus(500) }
    func version() async throws -> Version { throw ATCError.badStatus(500) }
    func sessions(includeArchived: Bool, status: SessionStatus?) async throws -> [Session] { [] }
    func session(id: String) async throws -> SessionDetail { throw ATCError.badStatus(500) }
    func startSession(_ request: StartSessionRequest) async throws -> SessionDetail { throw ATCError.badStatus(500) }
    func terminateSession(id: String) async throws -> SessionDetail { throw ATCError.badStatus(500) }
    func archiveSession(id: String) async throws -> SessionDetail { throw ATCError.badStatus(500) }
    func unarchiveSession(id: String) async throws -> SessionDetail { throw ATCError.badStatus(500) }
    func deleteSession(id: String) async throws { throw ATCError.badStatus(500) }
    func sendText(sessionID: String, text: String) async throws {}
    func sendKey(sessionID: String, key: String) async throws {}
    func actions() async throws -> [ATCAction] { [] }
    func action(name: String) async throws -> ATCAction { throw ATCError.badStatus(500) }
    func createAction(_ request: ActionWriteRequest) async throws -> ATCAction { throw ATCError.badStatus(500) }
    func updateAction(name: String, _ request: ActionWriteRequest) async throws -> ATCAction { throw ATCError.badStatus(500) }
    func setActionEnabled(name: String, enabled: Bool) async throws -> ATCAction { throw ATCError.badStatus(500) }
    func deleteAction(name: String) async throws { throw ATCError.badStatus(500) }
    func environments() async throws -> [ATCEnvironment] { [] }
    func listDirectory(path: String, showHidden: Bool) async throws -> DirectoryListing { throw ATCError.badStatus(500) }
    func project(id: String) async throws -> Project { throw ATCError.badStatus(500) }
    func createProject(name: String, workingDir: String) async throws -> Project { throw ATCError.badStatus(500) }
    func renameProject(id: String, name: String) async throws -> Project { throw ATCError.badStatus(500) }
    func archiveProject(id: String) async throws -> Project { throw ATCError.badStatus(500) }
    func unarchiveProject(id: String) async throws -> Project { throw ATCError.badStatus(500) }
    func projectSessions(projectID: String, includeArchived: Bool, status: SessionStatus?) async throws -> [Session] { [] }
    func deleteProject(id: String) async throws { throw ATCError.badStatus(500) }
    func workspaces(projectID: String?, includeArchived: Bool) async throws -> [Workspace] { [] }
    func workspace(id: String) async throws -> Workspace { throw ATCError.badStatus(500) }
    func createWorkspace(projectID: String, name: String) async throws -> Workspace { throw ATCError.badStatus(500) }
    func renameWorkspace(id: String, name: String) async throws -> Workspace { throw ATCError.badStatus(500) }
    func archiveWorkspace(id: String) async throws -> Workspace { throw ATCError.badStatus(500) }
    func unarchiveWorkspace(id: String) async throws -> Workspace { throw ATCError.badStatus(500) }
    func deleteWorkspace(id: String) async throws { throw ATCError.badStatus(500) }
    func workspaceSessions(workspaceID: String, includeArchived: Bool, status: SessionStatus?) async throws -> [Session] { [] }
    func attachURL(sessionID: String) -> URL { URL(string: "ws://127.0.0.1:1/attach")! }
    func attachHeaders() -> [String: String] { [:] }
}

/// Minimal stateful in-memory server for store tests: project mutations
/// persist, so the store's follow-up refreshes converge instead of racing
/// the assertions. Only the project methods are implemented.
final class StatefulProjectsClient: ATCClient, @unchecked Sendable {
    private let lock = NSLock()
    private var stored: [Project]
    private var counter = 0
    private let failAll: Bool

    init(failAll: Bool = false) {
        self.failAll = failAll
        self.stored = [
            Project(id: "prj_one", name: "One", workingDir: "/home/dev/one", createdAt: .now, updatedAt: .now),
            Project(id: "prj_two", name: "Two", workingDir: "/home/dev/two", createdAt: .now, updatedAt: .now),
        ]
    }

    private func withState<T>(_ body: (inout [Project]) throws -> T) throws -> T {
        if failAll { throw ATCError.badStatus(500) }
        lock.lock()
        defer { lock.unlock() }
        return try body(&stored)
    }

    func projects(includeArchived: Bool) async throws -> [Project] {
        try withState { $0.filter { includeArchived || !$0.isArchived } }
    }

    func project(id: String) async throws -> Project {
        try withState { state in
            guard let found = state.first(where: { $0.id == id }) else {
                throw ATCError.api(code: "project_not_found", message: id, sessionID: nil)
            }
            return found
        }
    }

    func createProject(name: String, workingDir: String) async throws -> Project {
        try withState { state in
            counter += 1
            let project = Project(
                id: "prj_stateful_\(counter)", name: name, workingDir: workingDir,
                createdAt: .now, updatedAt: .now
            )
            state.insert(project, at: 0)
            return project
        }
    }

    func renameProject(id: String, name: String) async throws -> Project {
        try mutate(id) { $0.name = name }
    }

    func archiveProject(id: String) async throws -> Project {
        try mutate(id) { $0.archivedAt = .now }
    }

    func unarchiveProject(id: String) async throws -> Project {
        try mutate(id) { $0.archivedAt = nil }
    }

    func deleteProject(id: String) async throws {
        try withState { state in
            guard state.contains(where: { $0.id == id }) else {
                throw ATCError.api(code: "project_not_found", message: id, sessionID: nil)
            }
            state.removeAll { $0.id == id }
        }
    }

    private func mutate(_ id: String, _ change: (inout Project) -> Void) throws -> Project {
        try withState { state in
            guard let index = state.firstIndex(where: { $0.id == id }) else {
                throw ATCError.api(code: "project_not_found", message: id, sessionID: nil)
            }
            change(&state[index])
            state[index].updatedAt = .now
            return state[index]
        }
    }

    // MARK: - Unused surface

    func health() async throws -> Health { throw ATCError.badStatus(500) }
    func version() async throws -> Version { throw ATCError.badStatus(500) }
    func sessions(includeArchived: Bool, status: SessionStatus?) async throws -> [Session] { [] }
    func session(id: String) async throws -> SessionDetail { throw ATCError.badStatus(500) }
    func startSession(_ request: StartSessionRequest) async throws -> SessionDetail { throw ATCError.badStatus(500) }
    func terminateSession(id: String) async throws -> SessionDetail { throw ATCError.badStatus(500) }
    func archiveSession(id: String) async throws -> SessionDetail { throw ATCError.badStatus(500) }
    func unarchiveSession(id: String) async throws -> SessionDetail { throw ATCError.badStatus(500) }
    func deleteSession(id: String) async throws { throw ATCError.badStatus(500) }
    func sendText(sessionID: String, text: String) async throws {}
    func sendKey(sessionID: String, key: String) async throws {}
    func actions() async throws -> [ATCAction] { [] }
    func action(name: String) async throws -> ATCAction { throw ATCError.badStatus(500) }
    func createAction(_ request: ActionWriteRequest) async throws -> ATCAction { throw ATCError.badStatus(500) }
    func updateAction(name: String, _ request: ActionWriteRequest) async throws -> ATCAction { throw ATCError.badStatus(500) }
    func setActionEnabled(name: String, enabled: Bool) async throws -> ATCAction { throw ATCError.badStatus(500) }
    func deleteAction(name: String) async throws { throw ATCError.badStatus(500) }
    func environments() async throws -> [ATCEnvironment] { [] }
    func listDirectory(path: String, showHidden: Bool) async throws -> DirectoryListing { throw ATCError.badStatus(500) }
    func projectSessions(projectID: String, includeArchived: Bool, status: SessionStatus?) async throws -> [Session] { [] }
    func workspaces(projectID: String?, includeArchived: Bool) async throws -> [Workspace] { [] }
    func workspace(id: String) async throws -> Workspace { throw ATCError.badStatus(500) }
    func createWorkspace(projectID: String, name: String) async throws -> Workspace { throw ATCError.badStatus(500) }
    func renameWorkspace(id: String, name: String) async throws -> Workspace { throw ATCError.badStatus(500) }
    func archiveWorkspace(id: String) async throws -> Workspace { throw ATCError.badStatus(500) }
    func unarchiveWorkspace(id: String) async throws -> Workspace { throw ATCError.badStatus(500) }
    func deleteWorkspace(id: String) async throws { throw ATCError.badStatus(500) }
    func workspaceSessions(workspaceID: String, includeArchived: Bool, status: SessionStatus?) async throws -> [Session] { [] }
    func attachURL(sessionID: String) -> URL { URL(string: "ws://127.0.0.1:1/attach")! }
    func attachHeaders() -> [String: String] { [:] }
}

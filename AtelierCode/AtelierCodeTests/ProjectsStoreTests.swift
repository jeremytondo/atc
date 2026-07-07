import Foundation
import Testing
import CockpitAPI
@testable import AtelierCode

/// ProjectsStore behavior against the canned mock fixtures: refresh,
/// archived filtering, and merge-after-mutation semantics.
@Suite("ProjectsStore")
struct ProjectsStoreTests {
    @Test("refresh loads active projects only by default")
    func refreshFiltersArchived() async throws {
        let store = ProjectsStore(client: MockCockpitClient())
        await store.refresh()
        #expect(store.hasLoadedOnce)
        #expect(store.lastError == nil)
        #expect(store.projects.count == 2)
        #expect(store.projects.allSatisfy { !$0.isArchived })
    }

    @Test("includeArchived surfaces archived projects")
    func includeArchived() async throws {
        let store = ProjectsStore(client: MockCockpitClient())
        store.includeArchived = true
        await store.refresh()
        #expect(store.projects.count == 3)
        #expect(store.projects.contains { $0.id == "prj_scratch" && $0.isArchived })
    }

    @Test("create merges the new project at the front")
    func createMerges() async throws {
        let store = ProjectsStore(client: MockCockpitClient())
        await store.refresh()
        let created = try await store.create(name: "Fresh", workingDir: "/home/dev/Projects/atelier")
        #expect(created.name == "Fresh")
        #expect(store.projects.first?.id == created.id)
    }

    @Test("archive drops the project from the default filter")
    func archiveDrops() async throws {
        let store = ProjectsStore(client: MockCockpitClient())
        await store.refresh()
        #expect(store.projects.contains { $0.id == "prj_atelier" })
        try await store.archive(id: "prj_atelier")
        #expect(!store.projects.contains { $0.id == "prj_atelier" })
    }

    @Test("rename updates the project in place")
    func renameUpdates() async throws {
        let store = ProjectsStore(client: MockCockpitClient())
        await store.refresh()
        try await store.rename(id: "prj_atelier", name: "Atelier Renamed")
        #expect(store.project(id: "prj_atelier")?.name == "Atelier Renamed")
    }

    @Test("failed refresh keeps the error and empty list")
    func refreshError() async throws {
        struct FailingClient: CockpitClient {
            func projects(includeArchived: Bool) async throws -> [Project] {
                throw CockpitError.badStatus(500)
            }
            // Everything else is unreachable in this test.
            func health() async throws -> Health { fatalError() }
            func version() async throws -> Version { fatalError() }
            func sessions(includeArchived: Bool, status: SessionStatus?) async throws -> [Session] { fatalError() }
            func session(id: String) async throws -> SessionDetail { fatalError() }
            func startSession(_ request: StartSessionRequest) async throws -> SessionDetail { fatalError() }
            func terminateSession(id: String) async throws -> SessionDetail { fatalError() }
            func archiveSession(id: String) async throws -> SessionDetail { fatalError() }
            func sendText(sessionID: String, text: String) async throws { fatalError() }
            func sendKey(sessionID: String, key: String) async throws { fatalError() }
            func actions() async throws -> [CockpitAction] { fatalError() }
            func environments() async throws -> [CockpitEnvironment] { fatalError() }
            func workspaceRoots() async throws -> [RemoteWorkspaceRoot] { fatalError() }
            func listDirectory(path: String, showHidden: Bool) async throws -> DirectoryListing { fatalError() }
            func project(id: String) async throws -> Project { fatalError() }
            func createProject(name: String, workingDir: String) async throws -> Project { fatalError() }
            func renameProject(id: String, name: String) async throws -> Project { fatalError() }
            func archiveProject(id: String) async throws -> Project { fatalError() }
            func unarchiveProject(id: String) async throws -> Project { fatalError() }
            func projectSessions(projectID: String, includeArchived: Bool, status: SessionStatus?) async throws -> [Session] { fatalError() }
            func attachURL(sessionID: String) -> URL { fatalError() }
            func attachHeaders() -> [String: String] { fatalError() }
        }
        let store = ProjectsStore(client: FailingClient())
        await store.refresh()
        #expect(store.lastError != nil)
        #expect(store.projects.isEmpty)
    }
}

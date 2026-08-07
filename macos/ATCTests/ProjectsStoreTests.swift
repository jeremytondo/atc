import Foundation
import Testing
import ATCAppServerAPI
@testable import ATC

/// ProjectsStore: refresh, and the merge placement every server-confirmed
/// mutation lands through.
@MainActor
@Suite("ProjectsStore")
struct ProjectsStoreTests {
    private func loadedStore() async -> (ProjectsStore, ScriptableAppServerClient) {
        let client = ScriptableAppServerClient()
        client.projects = [
            Fixtures.project(id: "prj_a", name: "Alpha", createdAt: Fixtures.date(10)),
            Fixtures.project(id: "prj_b", name: "Beta", createdAt: Fixtures.date(20)),
        ]
        let store = ProjectsStore(client: client)
        await store.refresh()
        return (store, client)
    }

    @Test("refresh loads projects newest-first")
    func refreshLoadsProjects() async {
        let (store, _) = await loadedStore()
        #expect(store.hasLoadedOnce)
        #expect(store.lastError == nil)
        #expect(store.projects.map(\.id) == ["prj_b", "prj_a"])
        #expect(store.project(id: "prj_a")?.name == "Alpha")
        #expect(store.project(id: "nope") == nil)
    }

    @Test("create merges the new project at the head with a distinct id")
    func createMerges() async throws {
        let (store, _) = await loadedStore()
        let first = try await store.create(name: "Fresh", defaultWorkingDirectory: "/home/dev/fresh")
        let second = try await store.create(name: "Fresher", defaultWorkingDirectory: "/home/dev/fresher")
        #expect(first.id != second.id)
        #expect(store.projects.first?.id == second.id)
        #expect(store.project(id: first.id)?.defaultWorkingDirectory == "/home/dev/fresh")
    }

    @Test("rename updates the project in place")
    func renameUpdates() async throws {
        let (store, _) = await loadedStore()
        try await store.rename(id: "prj_a", name: "Renamed")
        #expect(store.project(id: "prj_a")?.name == "Renamed")
        #expect(store.projects.map(\.id) == ["prj_b", "prj_a"])
    }

    @Test("delete removes the project row locally")
    func deleteRemoves() async throws {
        let (store, _) = await loadedStore()
        try await store.delete(id: "prj_b")
        #expect(store.projects.map(\.id) == ["prj_a"])
    }

    @Test("a failed refresh keeps the last-loaded list and records the error")
    func failedRefreshPreservesData() async {
        let (store, client) = await loadedStore()
        client.shouldFail = true
        await store.refresh()
        #expect(store.lastError != nil)
        #expect(store.projects.map(\.id) == ["prj_b", "prj_a"])
    }

    @Test("a slow refresh finishing after a newer one never clobbers it")
    func staleRefreshDoesNotClobber() async {
        let (store, client) = await loadedStore()
        let baseline = client.listProjectsCount
        client.delay = .milliseconds(200)
        let slow = Task { await store.refresh() }
        await settle(until: { client.listProjectsCount > baseline })

        client.delay = nil
        client.projects = [Fixtures.project(id: "prj_only", createdAt: Fixtures.date(90))]
        await store.refresh()
        #expect(store.projects.map(\.id) == ["prj_only"])

        await slow.value
        #expect(store.projects.map(\.id) == ["prj_only"])
        #expect(store.hasLoadedOnce)
    }
}

import ATCAppServerAPI
import Foundation
import Testing

@testable import ATC

/// TerminalsStore: the sidebar's standalone-terminal projection and
/// `checkLive`, the reconnect flow's authority on whether a terminal survived.
@MainActor
@Suite("TerminalsStore")
struct TerminalsStoreTests {
    private func loadedStore() async -> (TerminalsStore, ScriptableAppServerClient) {
        let client = ScriptableAppServerClient()
        client.projects = [
            Fixtures.project(id: "prj", createdAt: Fixtures.date(0)),
            Fixtures.project(id: "other", createdAt: Fixtures.date(1)),
        ]
        client.terminals = [
            Fixtures.terminal(id: "trm_new", createdAt: Fixtures.date(40)),
            Fixtures.terminal(id: "trm_old", createdAt: Fixtures.date(10)),
            Fixtures.terminal(id: "trm_ended", status: .ended, createdAt: Fixtures.date(20)),
            Fixtures.terminal(id: "trm_linked", threadId: "thr1", createdAt: Fixtures.date(30)),
            Fixtures.terminal(id: "trm_other", projectId: "other", createdAt: Fixtures.date(5)),
        ]
        let store = TerminalsStore(client: client)
        await store.refresh()
        return (store, client)
    }

    @Test("refresh loads the complete listing newest-first")
    func refreshLoadsEverything() async {
        let (store, _) = await loadedStore()
        #expect(store.hasLoadedOnce)
        #expect(store.lastError == nil)
        #expect(
            store.terminals.map(\.id)
                == ["trm_new", "trm_linked", "trm_ended", "trm_old", "trm_other"])
        #expect(store.terminal(id: "trm_old")?.id == "trm_old")
        #expect(store.terminal(id: "nope") == nil)
    }

    @Test("standaloneTerminals drops thread-linked terminals and orders oldest-first")
    func standaloneFiltersAndOrders() async {
        let (store, _) = await loadedStore()
        // Ended tombstones stay (the sidebar renders them); the thread's TUI
        // terminal and other projects' terminals do not.
        #expect(
            store.standaloneTerminals(projectID: "prj").map(\.id)
                == ["trm_old", "trm_ended", "trm_new"])
        #expect(store.standaloneTerminals(projectID: "other").map(\.id) == ["trm_other"])
        #expect(store.standaloneTerminals(projectID: "missing").isEmpty)
    }

    @Test("checkLive answers true, false, and nil for live, gone, and unreachable")
    func checkLivePaths() async {
        let (store, client) = await loadedStore()

        #expect(await store.checkLive(id: "trm_new") == true)
        #expect(await store.checkLive(id: "trm_ended") == false)
        // Unknown id: the server's 404 is authoritative, not an outage.
        #expect(await store.checkLive(id: "gone") == false)

        client.shouldFail = true
        // The server could not be asked; the caller keeps retrying instead of
        // presenting a dead terminal.
        #expect(await store.checkLive(id: "trm_new") == nil)
    }

    @Test("checkLive reconciles the fetched terminal back into the listing")
    func checkLiveMergesResult() async {
        let (store, client) = await loadedStore()
        client.terminals = client.terminals.map { terminal in
            guard terminal.id == "trm_new" else { return terminal }
            var updated = terminal
            updated.status = .ended
            return updated
        }
        #expect(await store.checkLive(id: "trm_new") == false)
        #expect(store.terminal(id: "trm_new")?.isLive == false)
    }

    @Test("create and delete merge the server's answer, never a guess")
    func mutationsMergeServerAnswers() async throws {
        let (store, _) = await loadedStore()
        let created = try await store.create(.init(projectId: "prj", name: "Build"))
        #expect(store.terminal(id: created.id)?.displayName == "Build")
        // Newest-first placement, same as a refresh would produce.
        #expect(store.terminals.first?.id == created.id)

        try await store.delete(id: created.id)
        #expect(store.terminal(id: created.id) == nil)
    }

    @Test("a failed refresh keeps the last-loaded listing and records the error")
    func failedRefreshPreservesData() async {
        let (store, client) = await loadedStore()
        client.shouldFail = true
        await store.refresh()
        #expect(store.lastError != nil)
        #expect(store.terminals.count == 5)
    }
}

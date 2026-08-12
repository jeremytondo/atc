import Foundation
import Testing
import ATCAppServerAPI
@testable import ATC

/// ThreadsStore: the active/archived split, the lazy archived load, and the
/// merge placement every server-confirmed mutation depends on. Mutations are
/// never optimistic, so each assertion is about where the *server's* answer
/// lands, not about a locally guessed state.
@MainActor
@Suite("ThreadsStore")
struct ThreadsStoreTests {
    private func loadedStore(
        _ client: ScriptableAppServerClient = ScriptableAppServerClient()
    ) async -> (ThreadsStore, ScriptableAppServerClient) {
        Fixtures.seed(client)
        let store = ThreadsStore(client: client)
        await store.refresh()
        return (store, client)
    }

    @Test("refresh loads active threads newest-first and leaves archived unfetched")
    func refreshSplitsActiveAndArchived() async {
        let (store, client) = await loadedStore()
        #expect(store.hasLoadedOnce)
        #expect(store.lastError == nil)
        #expect(store.threads.map(\.id) == ["thr3", "thr2", "thr1"])
        #expect(store.archivedThreads.isEmpty)
        #expect(!store.hasLoadedArchivedOnce)
        // Thread listings are priced: the unopened Archived filter costs one
        // request, not two.
        #expect(client.listThreadsCount == 1)
    }

    @Test("the archived list loads on demand and stays part of every refresh")
    func archivedLazyLoad() async {
        let (store, client) = await loadedStore()

        await store.loadArchivedIfNeeded()
        #expect(store.hasLoadedArchivedOnce)
        #expect(store.archivedThreads.map(\.id) == ["thr_archived"])
        let afterFirstLoad = client.listThreadsCount

        // A second request is a no-op; subsequent refreshes fetch both lists.
        await store.loadArchivedIfNeeded()
        #expect(client.listThreadsCount == afterFirstLoad)

        client.threads.append(Fixtures.thread(
            id: "thr_archived2",
            archivedAt: Fixtures.date(70),
            createdAt: Fixtures.date(6)
        ))
        await store.refresh()
        #expect(store.archivedThreads.map(\.id) == ["thr_archived2", "thr_archived"])
        #expect(client.listThreadsCount == afterFirstLoad + 2)
    }

    @Test("thread lookup spans both lists")
    func lookupSpansLists() async {
        let (store, _) = await loadedStore()
        await store.loadArchivedIfNeeded()
        #expect(store.thread(id: "thr2")?.id == "thr2")
        #expect(store.thread(id: "thr_archived")?.id == "thr_archived")
        #expect(store.thread(id: "nope") == nil)
    }

    @Test("pin and unpin keep the thread in the active list at its creation position")
    func pinKeepsCreationOrder() async throws {
        let (store, _) = await loadedStore()

        let pinned = try await store.pin(id: "thr1")
        #expect(pinned.isPinned)
        #expect(store.threads.map(\.id) == ["thr3", "thr2", "thr1"])
        #expect(store.thread(id: "thr1")?.isPinned == true)

        let unpinned = try await store.unpin(id: "thr1")
        #expect(!unpinned.isPinned)
        #expect(store.threads.map(\.id) == ["thr3", "thr2", "thr1"])
    }

    @Test("archive moves the thread to the archived list, newest archived first")
    func archiveMovesAndOrders() async throws {
        let (store, _) = await loadedStore()
        await store.loadArchivedIfNeeded()

        try await store.pin(id: "thr2")
        let archived = try await store.archive(id: "thr2")
        #expect(archived.isArchived)
        // Archiving drops the pin server-side; the store carries that answer.
        #expect(!archived.isPinned)
        #expect(store.threads.map(\.id) == ["thr3", "thr1"])
        #expect(store.archivedThreads.map(\.id) == ["thr2", "thr_archived"])

        try await store.archive(id: "thr3")
        #expect(store.threads.map(\.id) == ["thr1"])
        #expect(store.archivedThreads.map(\.id) == ["thr3", "thr2", "thr_archived"])
    }

    @Test("unarchive returns the thread to its creation position in the active list")
    func unarchiveRestoresCreationOrder() async throws {
        let (store, _) = await loadedStore()
        await store.loadArchivedIfNeeded()

        // thr_archived is the oldest thread in the fixture, so it lands last.
        let restored = try await store.unarchive(id: "thr_archived")
        #expect(!restored.isArchived)
        #expect(store.threads.map(\.id) == ["thr3", "thr2", "thr1", "thr_archived"])
        #expect(store.archivedThreads.isEmpty)
    }

    @Test("pinning an archived thread is refused and leaves both lists untouched")
    func pinArchivedRefused() async throws {
        let (store, _) = await loadedStore()
        await store.loadArchivedIfNeeded()

        await #expect(throws: (any Error).self) {
            try await store.pin(id: "thr_archived")
        }
        #expect(store.threads.map(\.id) == ["thr3", "thr2", "thr1"])
        #expect(store.archivedThreads.map(\.id) == ["thr_archived"])
    }

    @Test("delete removes the thread from whichever list holds it")
    func deleteRemovesFromBothLists() async throws {
        let (store, _) = await loadedStore()
        await store.loadArchivedIfNeeded()

        try await store.delete(id: "thr2")
        #expect(store.threads.map(\.id) == ["thr3", "thr1"])

        try await store.delete(id: "thr_archived")
        #expect(store.archivedThreads.isEmpty)
        #expect(store.thread(id: "thr_archived") == nil)
    }

    @Test("markViewed merges the server's cleared unread flag in place")
    func markViewedMerges() async throws {
        let client = ScriptableAppServerClient()
        Fixtures.seed(client)
        client.threads = client.threads.map { thread in
            guard thread.id == "thr2" else { return thread }
            var updated = thread
            updated.unread = true
            return updated
        }
        let store = ThreadsStore(client: client)
        await store.refresh()
        #expect(store.thread(id: "thr2")?.unread == true)

        let viewed = try await store.markViewed(id: "thr2")
        #expect(!viewed.unread)
        #expect(store.thread(id: "thr2")?.unread == false)
        // Merge keeps the creation ordering — viewing never reorders.
        #expect(store.threads.map(\.id) == ["thr3", "thr2", "thr1"])
    }

    @Test("create merges the new thread at the head of the active list")
    func createMerges() async throws {
        let (store, client) = await loadedStore()
        let created = try await store.create(.init(projectId: "prj", agentId: .claudeCode, name: "New"))
        #expect(store.threads.first?.id == created.id)
        #expect(client.createdThreadRequests.map(\.name) == ["New"])
        #expect(client.createdThreadRequests.map(\.agentId) == [.claudeCode])
    }

    @Test("a failed refresh keeps the last-loaded data and records the error")
    func failedRefreshPreservesData() async {
        let (store, client) = await loadedStore()
        client.shouldFail = true

        await store.refresh()
        #expect(store.lastError != nil)
        #expect(store.threads.map(\.id) == ["thr3", "thr2", "thr1"])

        client.shouldFail = false
        await store.refresh()
        #expect(store.lastError == nil)
    }

    @Test("a slow refresh finishing after a newer one never clobbers it")
    func staleRefreshDoesNotClobber() async {
        let client = ScriptableAppServerClient()
        let (store, _) = await loadedStore(client)

        let baseline = client.listThreadsCount
        client.delay = .milliseconds(200)
        let slow = Task { await store.refresh() }
        // The client counts a request before it parks, so this proves the
        // slow refresh already claimed the older generation and snapshotted
        // the pre-mutation thread list.
        await settle(until: { client.listThreadsCount > baseline })

        client.delay = nil
        client.threads = [Fixtures.thread(id: "thr_only", createdAt: Fixtures.date(90))]
        await store.refresh()
        #expect(store.threads.map(\.id) == ["thr_only"])

        await slow.value
        #expect(store.threads.map(\.id) == ["thr_only"])
        #expect(store.hasLoadedOnce)
    }

    @Test("a modeled failure surfaces the server's message, not a status dump")
    func openTerminalSurfacesServerMessage() async {
        let (store, client) = await loadedStore()
        client.openThreadTerminalFailure = .init(
            _tag: .providerUnavailable,
            agentId: "codex",
            reason: "install the Codex CLI or set codexExecutable",
            message: "install the Codex CLI or set codexExecutable"
        )
        do {
            _ = try await store.openTerminal(threadID: "thr1")
            Issue.record("openTerminal should have thrown")
        } catch {
            // The whole point of the unwrap seam: the alert text IS the
            // server's actionable guidance.
            #expect(
                error.localizedDescription == "install the Codex CLI or set codexExecutable"
            )
        }
    }

    @Test("openTerminal is idempotent: a live linked terminal is returned as-is")
    func openTerminalIsIdempotent() async throws {
        let (store, client) = await loadedStore()
        let first = try await store.openTerminal(threadID: "thr1")
        let second = try await store.openTerminal(threadID: "thr1")
        #expect(first.id == second.id)
        #expect(first.threadId == "thr1")
        #expect(first.isLive)
        #expect(client.openThreadTerminalCount == 2)
        #expect(client.terminals.filter { $0.threadId == "thr1" }.count == 1)
    }
}

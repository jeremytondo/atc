import ATCAppServerAPI
import Foundation
import Testing

@testable import ATC

/// Runtime lifecycle (add / name-only edit / rebuild / delete), thread
/// opening, and the terminal-lifecycle reconciliation that must never fire on
/// a failed refresh.
@MainActor
@Suite("AppModel runtimes")
struct AppModelRuntimeTests {
    // MARK: - Connection lifecycle

    @Test("adding a connection creates exactly one runtime, gray until the stream settles")
    func addCreatesRuntime() throws {
        let model = makeEmptyAppModel()
        #expect(model.runtimes.isEmpty)
        let record = try model.addConnection(name: "A", urlString: "http://a:1", token: "")
        #expect(model.runtimes.map(\.id) == [record.id])
        #expect(model.reachability(of: record.id) == .unknown)
        #expect(!model.canMutate(connectionID: record.id))
        // A Connection with no runtime (an unsaved draft) is unknown, not red.
        #expect(model.reachability(of: UUID()) == .unknown)
    }

    @Test("name-only edit keeps the same runtime and store instances")
    func nameOnlyEditKeepsRuntime() throws {
        let model = makeEmptyAppModel()
        let record = try model.addConnection(name: "A", urlString: "http://a:1", token: "")
        let runtime = try #require(model.runtime(id: record.id))
        let threads = runtime.threads

        #expect(!model.wouldRebuildConnection(id: record.id, urlString: "http://a:1", token: ""))
        try model.updateConnection(id: record.id, name: "Renamed", urlString: "http://a:1", token: "")
        #expect(model.runtime(id: record.id) === runtime)
        #expect(model.runtime(id: record.id)?.threads === threads)
        #expect(model.runtime(id: record.id)?.record.name == "Renamed")
    }

    @Test("a URL change rebuilds only the affected runtime and preserves order")
    func urlChangeRebuildsOne() throws {
        let model = makeEmptyAppModel()
        let a = try model.addConnection(name: "A", urlString: "http://a:1", token: "")
        let b = try model.addConnection(name: "B", urlString: "http://b:1", token: "")
        let runtimeA = try #require(model.runtime(id: a.id))
        let runtimeB = try #require(model.runtime(id: b.id))

        #expect(model.wouldRebuildConnection(id: a.id, urlString: "http://a:2", token: ""))
        try model.updateConnection(id: a.id, name: "A", urlString: "http://a:2", token: "")
        #expect(model.runtime(id: a.id) !== runtimeA)
        #expect(model.runtime(id: a.id)?.record.urlString == "http://a:2")
        #expect(model.runtime(id: b.id) === runtimeB)
        #expect(model.runtimes.map(\.id) == [a.id, b.id])
    }

    @Test("a token change rebuilds; a normalization-only change does not")
    func tokenChangeRebuilds() throws {
        let model = makeEmptyAppModel()
        let a = try model.addConnection(name: "A", urlString: "http://a:1", token: "")
        #expect(model.wouldRebuildConnection(id: a.id, urlString: "http://a:1", token: "secret"))
        // "a:1" normalizes to "http://a:1": the same origin, so no rebuild.
        #expect(!model.wouldRebuildConnection(id: a.id, urlString: "a:1", token: ""))

        let runtime = try #require(model.runtime(id: a.id))
        try model.updateConnection(id: a.id, name: "A", urlString: "http://a:1", token: "secret")
        #expect(model.runtime(id: a.id) !== runtime)
        #expect(model.runtime(id: a.id)?.transportHeaders == ["Authorization": "Bearer secret"])
    }

    @Test("deleting a connection removes only its runtime")
    func deleteRemovesOwnRuntime() throws {
        let model = makeEmptyAppModel()
        let a = try model.addConnection(name: "A", urlString: "http://a:1", token: "")
        let b = try model.addConnection(name: "B", urlString: "http://b:1", token: "")
        model.removeConnection(id: a.id)
        #expect(model.runtimes.map(\.id) == [b.id])
        #expect(model.runtime(id: a.id) == nil)
    }

    @Test("the navigation snapshot follows the runtime list and store state")
    func navigationSnapshot() async throws {
        let client = ScriptableAppServerClient()
        Fixtures.seed(client)
        // A failed first-contact probe leaves nothing current, so the window
        // treats every reference as unresolved rather than deleted.
        client.shouldFail = true
        let test = try await makeModel(client: client)
        let cold = test.model.windowNavigationSnapshot()
        #expect(cold.connections.map(\.id) == [test.connectionID])
        #expect(!cold.connections[0].threadsCurrent)
        #expect(!cold.connections[0].terminalsCurrent)

        client.shouldFail = false
        await test.runtime.refresh()
        await test.runtime.threads.loadArchivedIfNeeded()
        let warm = test.model.windowNavigationSnapshot()
        #expect(warm.connections[0].threadsCurrent)
        #expect(warm.connections[0].terminalsCurrent)
        // Both lists feed the snapshot so archived threads reconcile too.
        #expect(
            Set(warm.connections[0].threads.map(\.id))
                == ["thr1", "thr2", "thr3", "thr_archived"])
        #expect(warm.connections[0].threads.filter(\.isArchived).map(\.id) == ["thr_archived"])
        #expect(Set(warm.connections[0].terminals.map(\.id)) == ["trm_live", "trm_ended"])
        #expect(warm.connections[0].terminals.filter(\.isLive).map(\.id) == ["trm_live"])

        test.model.removeConnection(id: test.connectionID)
        #expect(test.model.windowNavigationSnapshot().connections.isEmpty)
    }

    @Test("a refresh in flight when its connection is rebuilt lands harmlessly")
    func staleRefreshHarmless() async throws {
        let client = ScriptableAppServerClient()
        Fixtures.seed(client)
        let test = try await makeModel(client: client)
        client.delay = .milliseconds(200)
        let oldRuntime = test.runtime
        let slowRefresh = Task { await oldRuntime.refresh() }
        await settle(until: { client.listThreadsCount > 0 })

        client.delay = nil
        try test.model.updateConnection(
            id: test.connectionID, name: "A", urlString: "http://a:2", token: ""
        )
        let newRuntime = test.runtime
        #expect(newRuntime !== oldRuntime)

        await slowRefresh.value
        // The late result mutated only the discarded runtime's stores; the
        // rebuilt runtime's own first-contact probe loads current data.
        #expect(test.runtime === newRuntime)
        await settle(until: { newRuntime.threads.hasLoadedOnce })
        #expect(newRuntime.threads.threads.count == 3)
        #expect(test.model.runtimes.count == 1)
    }

    // MARK: - Opening threads

    @Test("openThread opens the TUI terminal, merges it, and attaches its controller")
    func openThreadAttaches() async throws {
        let client = ScriptableAppServerClient()
        Fixtures.seed(client)
        let test = try await makeModel(client: client)
        await test.runtime.refresh()

        let ref = try await test.model.openThread(test.threadRef("thr1"))
        #expect(ref.connectionID == test.connectionID)
        #expect(test.model.terminals[ref] != nil)
        #expect(test.runtime.terminals.terminal(id: ref.terminalID)?.threadId == "thr1")
        #expect(test.attach.attemptCount == 1)

        // Idempotent: re-opening reuses the same terminal and controller.
        let controller = try #require(test.model.terminals[ref])
        let again = try await test.model.openThread(test.threadRef("thr1"))
        #expect(again == ref)
        #expect(test.model.terminals[ref] === controller)
        #expect(test.attach.attemptCount == 1)
    }

    @Test("openThread against a removed connection reports the connection is gone")
    func openThreadWithoutRuntime() async throws {
        let model = makeEmptyAppModel()
        await #expect(throws: AppServerUnavailable.self) {
            try await model.openThread(ThreadRef(connectionID: UUID(), threadID: "thr1"))
        }
    }

    @Test("a failed open leaves no controller behind")
    func openThreadFailure() async throws {
        let client = ScriptableAppServerClient()
        Fixtures.seed(client)
        let test = try await makeModel(client: client)
        await test.runtime.refresh()

        client.shouldFail = true
        await #expect(throws: (any Error).self) {
            try await test.model.openThread(test.threadRef("thr1"))
        }
        #expect(test.model.terminals.isEmpty)
    }

    // MARK: - Terminal lifecycle reconciliation

    @Test("a successful refresh that reports the terminal ended tears the attach down")
    func reconcileDisconnectsEndedTerminal() async throws {
        let client = ScriptableAppServerClient()
        Fixtures.seed(client)
        let test = try await makeModel(client: client)
        await test.runtime.refresh()

        let terminal = try #require(test.runtime.terminals.terminal(id: "trm_live"))
        test.model.attachIfNeeded(to: terminal, connectionID: test.connectionID)
        let ref = test.terminalRef("trm_live")
        #expect(test.model.terminals[ref] != nil)

        client.terminals = client.terminals.map { existing in
            guard existing.id == "trm_live" else { return existing }
            var ended = existing
            ended.status = .ended
            return ended
        }
        await test.runtime.terminals.refresh()
        test.model.reconcileTerminalLifecycle()
        // The controller stays registered so the final frame survives under
        // the relaunch affordance; only its socket and retries stop.
        let controller = try #require(test.model.terminals[ref])
        #expect(controller.phase == .ended(.terminalEnded))
        #expect(!controller.isActivelyAttached)
    }

    @Test("a failed refresh never manufactures a terminal lifecycle transition")
    func reconcileIgnoresFailedRefresh() async throws {
        let client = ScriptableAppServerClient()
        Fixtures.seed(client)
        let test = try await makeModel(client: client)
        await test.runtime.refresh()

        let terminal = try #require(test.runtime.terminals.terminal(id: "trm_live"))
        test.model.attachIfNeeded(to: terminal, connectionID: test.connectionID)
        let ref = test.terminalRef("trm_live")

        client.shouldFail = true
        await test.runtime.terminals.refresh()
        #expect(test.runtime.terminals.lastError != nil)

        test.model.reconcileTerminalLifecycle()
        let controller = try #require(test.model.terminals[ref])
        #expect(controller.isActivelyAttached)
    }

    @Test("an ended terminal is never attached; a live one is")
    func attachRequiresLiveness() async throws {
        let client = ScriptableAppServerClient()
        Fixtures.seed(client)
        let test = try await makeModel(client: client)
        await test.runtime.refresh()

        let ended = try #require(test.runtime.terminals.terminal(id: "trm_ended"))
        test.model.attachIfNeeded(to: ended, connectionID: test.connectionID)
        #expect(test.model.terminals[test.terminalRef("trm_ended")] == nil)

        let live = try #require(test.runtime.terminals.terminal(id: "trm_live"))
        test.model.attachIfNeeded(to: live, connectionID: test.connectionID)
        #expect(test.model.terminals[test.terminalRef("trm_live")] != nil)
    }

    @Test("removing a connection disconnects its terminals")
    func removingConnectionTearsDownTerminals() async throws {
        let client = ScriptableAppServerClient()
        Fixtures.seed(client)
        let test = try await makeModel(client: client)
        await test.runtime.refresh()

        let terminal = try #require(test.runtime.terminals.terminal(id: "trm_live"))
        test.model.attachIfNeeded(to: terminal, connectionID: test.connectionID)
        let ref = test.terminalRef("trm_live")
        let controller = try #require(test.model.terminals[ref])

        test.model.removeConnection(id: test.connectionID)
        #expect(test.model.terminals[ref] == nil)
        #expect(!controller.isActivelyAttached)
    }

    @Test("liveness affordances follow controller phase, not registry membership")
    func livenessFollowsPhase() async throws {
        let client = ScriptableAppServerClient()
        Fixtures.seed(client)
        let test = try await makeModel(client: client)
        await test.runtime.refresh()

        let terminal = try #require(test.runtime.terminals.terminal(id: "trm_live"))
        test.model.attachIfNeeded(to: terminal, connectionID: test.connectionID)
        let ref = test.terminalRef("trm_live")
        let controller = try #require(test.model.terminals[ref])
        #expect(test.model.hasLiveTerminals(connectionID: test.connectionID))
        #expect(test.model.activelyAttachedRefs.contains(ref))

        // Ending the attach retains the controller for scrollback but drops
        // it from every liveness affordance.
        controller.disconnect()
        #expect(test.model.terminals[ref] != nil)
        #expect(!test.model.hasLiveTerminals(connectionID: test.connectionID))
        #expect(!test.model.activelyAttachedRefs.contains(ref))
    }
}

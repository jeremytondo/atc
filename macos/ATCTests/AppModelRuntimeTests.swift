import Foundation
import Testing
import ATCAPI
@testable import ATC

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

    @Test("deleting a connection removes only its runtime")
    func deleteRemovesOwnRuntime() throws {
        let (model, _) = makeModel()
        let a = try model.addConnection(name: "A", urlString: "http://a:1", token: "")
        let b = try model.addConnection(name: "B", urlString: "http://b:1", token: "")
        model.removeConnection(id: a.id)
        #expect(model.runtimes.map(\.id) == [b.id])
    }

    @Test("deleting a connection keeps the other runtime")
    func deleteKeepsForeignRuntime() throws {
        let (model, _) = makeModel()
        let a = try model.addConnection(name: "A", urlString: "http://a:1", token: "")
        let b = try model.addConnection(name: "B", urlString: "http://b:1", token: "")
        model.removeConnection(id: a.id)
        #expect(model.runtime(id: b.id) != nil)
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

    @Test("navigation snapshot drops a deleted connection")
    func snapshotDropsDeletedConnection() throws {
        let (model, _) = makeModel()
        let a = try model.addConnection(name: "A", urlString: "http://a:1", token: "")
        let b = try model.addConnection(name: "B", urlString: "http://b:1", token: "")
        #expect(model.windowNavigationSnapshot().connections.map(\.id) == [a.id, b.id])
        model.removeConnection(id: b.id)
        #expect(model.windowNavigationSnapshot().connections.map(\.id) == [a.id])
    }

    @Test("runtime refresh loads workspaces and actions alongside projects and sessions")
    func refreshLoadsWorkspacesAndActions() async throws {
        let (model, _) = makeModel()
        let a = try model.addConnection(name: "A", urlString: "http://a:1", token: "")
        let runtime = model.runtime(id: a.id)!
        runtime.stopPolling()
        await runtime.refresh()
        #expect(!runtime.workspaces.workspaces.isEmpty)
        #expect(!runtime.actions.actions.isEmpty)
        #expect(runtime.sessions.sessions.contains { $0.status == .ended })
    }
}

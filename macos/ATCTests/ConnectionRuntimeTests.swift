import Foundation
import Testing
import ATCAppServerAPI
import ATCAppServerTransport
@testable import ATC

/// The SSE-driven liveness model: `.connected` resyncs everything, `.change`
/// events coalesce into one refetch per named collection, `.disconnected` and
/// failed refreshes turn the Connection red without discarding its data.
@MainActor
@Suite("ConnectionRuntime")
struct ConnectionRuntimeTests {
    private func makeRuntime(
        client: ScriptableAppServerClient,
        events: ScriptedEventStream
    ) -> ConnectionRuntime {
        ConnectionRuntime(
            record: ConnectionRecord(name: "A", urlString: "http://a:1", token: ""),
            client: client,
            baseURL: URL(string: "http://a:1")!,
            eventStreamFactory: { _, _ in events.stream }
        )
    }

    @Test("connected resyncs every store and turns the Connection green")
    func connectedRefreshesEverything() async {
        let client = ScriptableAppServerClient()
        Fixtures.seed(client)
        let events = ScriptedEventStream()
        let runtime = makeRuntime(client: client, events: events)
        defer { runtime.stop() }

        runtime.start()
        #expect(runtime.reachability == .unknown)

        events.connect()
        await settle(until: { runtime.reachability == .connected })

        #expect(runtime.reachability == .connected)
        #expect(runtime.projects.projects.map(\.id) == ["prj"])
        #expect(runtime.threads.threads.map(\.id) == ["thr3", "thr2", "thr1"])
        #expect(runtime.terminals.terminals.count == 2)
        #expect(runtime.agents.agents.count == 2)
    }

    @Test("a burst of change events coalesces into one refetch per collection")
    func changeEventsCoalesce() async {
        let client = ScriptableAppServerClient()
        Fixtures.seed(client)
        let events = ScriptedEventStream()
        let runtime = makeRuntime(client: client, events: events)
        defer { runtime.stop() }

        runtime.start()
        events.connect()
        await settle(until: { runtime.reachability == .connected })
        let threadsBaseline = client.listThreadsCount
        let projectsBaseline = client.listProjectsCount

        client.threads.append(Fixtures.thread(id: "thr4", createdAt: Fixtures.date(35)))
        events.change(.thread, id: "thr4", change: .created)
        events.change(.thread, id: "thr4")
        events.change(.thread, id: "thr3")

        await settle(until: { runtime.threads.threads.contains { $0.id == "thr4" } })

        // Three invalidations, one refetch — and nothing the events did not
        // name is refetched.
        #expect(client.listThreadsCount == threadsBaseline + 1)
        #expect(client.listProjectsCount == projectsBaseline)
    }

    @Test("a change event refetches only the collection it names")
    func changeEventScopesRefetch() async {
        let client = ScriptableAppServerClient()
        Fixtures.seed(client)
        let events = ScriptedEventStream()
        let runtime = makeRuntime(client: client, events: events)
        defer { runtime.stop() }

        runtime.start()
        events.connect()
        await settle(until: { runtime.reachability == .connected })
        let threadsBaseline = client.listThreadsCount

        client.terminals.append(Fixtures.terminal(id: "trm_new", createdAt: Fixtures.date(80)))
        events.change(.terminal, id: "trm_new", change: .created)
        await settle(until: { runtime.terminals.terminals.contains { $0.id == "trm_new" } })

        #expect(client.listThreadsCount == threadsBaseline)
    }

    @Test("disconnected marks the Connection unreachable and keeps its data")
    func disconnectedKeepsData() async {
        let client = ScriptableAppServerClient()
        Fixtures.seed(client)
        let events = ScriptedEventStream()
        let runtime = makeRuntime(client: client, events: events)
        defer { runtime.stop() }

        runtime.start()
        events.connect()
        await settle(until: { runtime.reachability == .connected })

        events.disconnect()
        await settle(until: { runtime.reachability == .unreachable })
        #expect(runtime.threads.threads.count == 3)
        #expect(runtime.projects.projects.count == 1)

        // A later reconnect resyncs and goes green again.
        events.connect()
        await settle(until: { runtime.reachability == .connected })
    }

    @Test("a failed refresh is unreachable, and recovery is one clean refresh away")
    func failedRefreshIsUnreachable() async {
        let client = ScriptableAppServerClient()
        Fixtures.seed(client)
        let events = ScriptedEventStream()
        let runtime = makeRuntime(client: client, events: events)
        defer { runtime.stop() }

        // Green requires the event stream: without it no invalidations flow,
        // so successful HTTP alone must never claim .connected.
        await runtime.refresh()
        #expect(runtime.reachability == .unreachable)

        runtime.start()
        events.connect()
        await settle(until: { runtime.reachability == .connected })

        client.shouldFail = true
        await runtime.refresh()
        #expect(runtime.reachability == .unreachable)
        #expect(runtime.threads.threads.count == 3)
        #expect(runtime.terminals.terminals.count == 2)

        client.shouldFail = false
        await runtime.refresh()
        #expect(runtime.reachability == .connected)
    }

    @Test("a name-only record update leaves the client and stores in place")
    func updateRecordKeepsStores() {
        let client = ScriptableAppServerClient()
        let events = ScriptedEventStream()
        let runtime = makeRuntime(client: client, events: events)
        defer { runtime.stop() }

        let threads = runtime.threads
        runtime.updateRecord(ConnectionRecord(
            id: runtime.id,
            name: "Renamed",
            urlString: runtime.record.urlString,
            token: ""
        ))
        #expect(runtime.record.name == "Renamed")
        #expect(runtime.threads === threads)
    }

    @Test("a token turns into the transport's bearer header; an empty one adds nothing")
    func transportHeaders() {
        let events = ScriptedEventStream()
        let bare = makeRuntime(client: ScriptableAppServerClient(), events: events)
        defer { bare.stop() }
        #expect(bare.transportHeaders.isEmpty)

        let authorized = ConnectionRuntime(
            record: ConnectionRecord(name: "A", urlString: "http://a:1", token: "secret"),
            client: ScriptableAppServerClient(),
            baseURL: URL(string: "http://a:1")!,
            eventStreamFactory: { _, _ in ScriptedEventStream.inert() }
        )
        defer { authorized.stop() }
        #expect(authorized.transportHeaders == ["Authorization": "Bearer secret"])
    }
}

import Foundation
import Testing

@testable import ATCAppServerTransport

private let eventsURL = URL(string: "http://127.0.0.1:7332/api/v1/events")!

@Suite("Resource event stream")
struct ResourceEventStreamTests {
    @Test("connected precedes changes, and reconnects resignal connected")
    func connectAndResync() async {
        let connector = ScriptedConnector([
            .stream(
                [": connected\n\n", "data: {\"resource\":\"thread\",\"id\":\"t1\",\"change\":\"updated\"}\n\n"],
                thenHang: false
            ),
            .stream([": connected\n\n"], thenHang: true),
        ])
        let stream = ResourceEventStream.stream(
            url: eventsURL, configuration: fastConfiguration(), connector: connector.connect()
        )

        let events = await collect(stream, count: 4)

        #expect(events.count == 4)
        #expect(events.first == .connected)
        guard case .change(let change) = events[1] else {
            Issue.record("expected a change event, got \(events[1])")
            return
        }
        #expect(change.id == "t1")
        #expect(events[2] == .disconnected)
        // The reconnect emitted connected again: the consumer's cue to
        // refetch, since delivery is ephemeral with no replay.
        #expect(events[3] == .connected)
    }

    @Test("a silent stream is torn down and reconnected")
    func silenceTimeout() async {
        let connector = ScriptedConnector([
            .stream([": connected\n\n"], thenHang: true),
            .stream([": connected\n\n"], thenHang: true),
        ])
        let stream = ResourceEventStream.stream(
            url: eventsURL,
            configuration: fastConfiguration(silenceTimeout: .milliseconds(50)),
            connector: connector.connect()
        )

        let events = await collect(stream, count: 3)

        #expect(events == [.connected, .disconnected, .connected])
    }

    @Test("failed connects retry silently until one opens")
    func failedConnectsStaySilent() async {
        let connector = ScriptedConnector([
            .rejected(404),
            .failure,
            .stream([": connected\n\n"], thenHang: true),
        ])
        let stream = ResourceEventStream.stream(
            url: eventsURL, configuration: fastConfiguration(), connector: connector.connect()
        )

        let events = await collect(stream, count: 1)

        // No phantom connect/disconnect cycles from attempts that never
        // reached the opening comment.
        #expect(events == [.connected])
        #expect(connector.attempts == 3)
    }

    @Test("headers reach the connector — the bearer-auth seam")
    func headersPassThrough() async {
        let connector = ScriptedConnector([.stream([": connected\n\n"], thenHang: true)])
        let stream = ResourceEventStream.stream(
            url: eventsURL,
            headers: ["Authorization": "Bearer token-1"],
            configuration: fastConfiguration(),
            connector: connector.connect()
        )

        _ = await collect(stream, count: 1)

        #expect(connector.headers == ["Authorization": "Bearer token-1"])
    }

    @Test("heartbeats keep the stream alive without surfacing events")
    func heartbeatsAreInvisible() async {
        let connector = ScriptedConnector([
            .stream(
                [
                    ": connected\n\n",
                    ": heartbeat\n\n",
                    ": heartbeat\n\n",
                    "data: {\"resource\":\"project\",\"id\":\"p1\",\"change\":\"created\"}\n\n",
                ],
                thenHang: true
            )
        ])
        let stream = ResourceEventStream.stream(
            url: eventsURL, configuration: fastConfiguration(), connector: connector.connect()
        )

        let events = await collect(stream, count: 2)

        #expect(events.first == .connected)
        guard case .change(let change) = events[1] else {
            Issue.record("expected a change event, got \(events[1])")
            return
        }
        #expect(change.id == "p1")
    }

    @Test("undecodable payloads are dropped, not fatal")
    func undecodablePayloadDropped() async {
        let connector = ScriptedConnector([
            .stream(
                [
                    ": connected\n\n",
                    "data: {\"resource\":\"galaxy\",\"id\":\"g1\",\"change\":\"updated\"}\n\n",
                    "data: {\"resource\":\"terminal\",\"id\":\"term1\",\"change\":\"deleted\"}\n\n",
                ],
                thenHang: true
            )
        ])
        let stream = ResourceEventStream.stream(
            url: eventsURL, configuration: fastConfiguration(), connector: connector.connect()
        )

        let events = await collect(stream, count: 2)

        #expect(events.first == .connected)
        guard case .change(let change) = events[1] else {
            Issue.record("expected a change event, got \(events[1])")
            return
        }
        #expect(change.id == "term1")
    }
}

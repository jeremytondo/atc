import Foundation
import Testing
@testable import ATCAppServerTransport

/// Scripted stand-in for the live URLSession connector: each connection
/// attempt pops the next script; an exhausted script hangs (a quiet, open
/// stream) so a test never spins through unplanned reconnects.
private final class ScriptedConnector: @unchecked Sendable {
    enum Connection {
        /// The connect call itself throws.
        case failure
        /// Connects with a non-200 status.
        case rejected(Int)
        /// Serves the chunks, then either ends the stream or holds it open.
        case stream([String], thenHang: Bool)
    }

    private let lock = NSLock()
    private var script: [Connection]
    private var recordedAttempts = 0

    var attempts: Int { lock.withLock { recordedAttempts } }

    init(_ script: [Connection]) {
        self.script = script
    }

    private func next() -> Connection {
        lock.lock()
        defer { lock.unlock() }
        recordedAttempts += 1
        return script.isEmpty ? .stream([], thenHang: true) : script.removeFirst()
    }

    func connect() -> ResourceEventStream.Connector {
        { _, _ in
            switch self.next() {
            case .failure:
                throw URLError(.cannotConnectToHost)
            case .rejected(let status):
                return (status, AsyncThrowingStream { $0.finish() })
            case .stream(let chunks, let thenHang):
                return (
                    200,
                    AsyncThrowingStream { continuation in
                        for chunk in chunks {
                            continuation.yield(Array(chunk.utf8))
                        }
                        if !thenHang {
                            continuation.finish()
                        }
                    }
                )
            }
        }
    }
}

private func fastConfiguration(silenceTimeout: Duration = .seconds(5)) -> ResourceEventStream.Configuration {
    var configuration = ResourceEventStream.Configuration()
    configuration.silenceTimeout = silenceTimeout
    configuration.reconnectBaseDelay = .milliseconds(1)
    configuration.reconnectMaxDelay = .milliseconds(5)
    return configuration
}

private let eventsURL = URL(string: "http://127.0.0.1:7332/api/v1/events")!

/// Consumes until `count` events arrive; a deadline turns a hung stream into
/// a visible assertion failure instead of a stuck suite.
private func collect(
    _ stream: AsyncStream<ResourceEventStream.Event>,
    count: Int
) async -> [ResourceEventStream.Event] {
    let collector = Task {
        var events: [ResourceEventStream.Event] = []
        for await event in stream {
            events.append(event)
            if events.count == count { break }
        }
        return events
    }
    let deadline = Task {
        try? await Task.sleep(for: .seconds(10))
        collector.cancel()
    }
    let events = await collector.value
    deadline.cancel()
    return events
}

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
            ),
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
            ),
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

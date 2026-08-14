// ResourceEventStream: the App Server's SSE resource-change stream
// (`GET /api/v1/events`) as a long-lived AsyncStream.
//
// The contract's invariants, all owned here so consumers can't get them
// wrong:
// - `.connected` is emitted only after the stream's opening `: connected`
//   comment — the server guarantees a subscriber registered before that byte,
//   so refetching lists on `.connected` is race-free. Fetching earlier leaves
//   a missed-event window.
// - Delivery is ephemeral with no replay: every reconnect emits `.connected`
//   again and the consumer must resync by refetching, never resume.
// - Heartbeat comments arrive every ~25 s; a stream silent well past that is
//   dead. The watchdog tears it down and reconnects with backoff. Backoff
//   only resets once a connection has proven stable — a server that greets
//   and immediately drops must not induce a hot reconnect loop, because
//   every subscribe runs a terminal reconciliation server-side.
// - Events are thin invalidations; payloads that fail to decode are dropped
//   (a decode failure means contract drift, which regeneration catches).
//
// The stream retries forever; it ends only when the consuming task is
// cancelled. `.disconnected` marks each loss so consumers can surface
// reachability, but reachability policy lives above this seam.

import ATCAppServerAPI
import Foundation

public enum ResourceEventStream {
    public typealias Change = Components.Schemas.ResourceChangedEvent

    public enum Event: Sendable, Equatable {
        /// Stream is open and reconciled server-side: refetch everything now.
        case connected
        /// A named resource changed: coalesce and refetch what it names.
        case change(Change)
        /// Stream lost; a reconnect attempt follows automatically.
        case disconnected
    }

    public struct Configuration: Sendable {
        /// Heartbeats arrive every ~25 s; 60 s of silence means dead.
        public var silenceTimeout: Duration = .seconds(60)
        public var reconnectBaseDelay: Duration = .milliseconds(500)
        public var reconnectMaxDelay: Duration = .seconds(8)

        public init() {}
    }

    /// A single connection attempt: HTTP status plus the raw byte chunks.
    /// Injected in tests; live path wraps URLSession.bytes.
    typealias Connector =
        @Sendable (URL, [String: String]) async throws
        -> (statusCode: Int, chunks: AsyncThrowingStream<[UInt8], any Error>)

    public static func live(
        baseURL: URL,
        headers: [String: String] = [:],
        configuration: Configuration = Configuration()
    ) -> AsyncStream<Event> {
        stream(
            url: baseURL.appending(path: "api/v1/events"),
            headers: headers,
            configuration: configuration,
            connector: liveConnector(configuration: configuration)
        )
    }

    static func stream(
        url: URL,
        headers: [String: String] = [:],
        configuration: Configuration = Configuration(),
        connector: @escaping Connector
    ) -> AsyncStream<Event> {
        AsyncStream { continuation in
            let task = Task {
                let backoff = ReconnectBackoff(
                    baseDelay: configuration.reconnectBaseDelay,
                    maximumDelay: configuration.reconnectMaxDelay
                )
                var attempt = 0
                while !Task.isCancelled {
                    let clock = ContinuousClock()
                    let opened = clock.now
                    let sawConnected = await runConnection(
                        url: url,
                        headers: headers,
                        configuration: configuration,
                        connector: connector,
                        continuation: continuation
                    )
                    if Task.isCancelled { break }
                    if sawConnected {
                        continuation.yield(.disconnected)
                    }
                    // Only a connection that lived a while proves the server
                    // healthy; a greet-then-drop cycle keeps escalating.
                    let wasStable =
                        sawConnected
                        && clock.now - opened >= configuration.reconnectMaxDelay
                    attempt = wasStable ? 0 : attempt + 1
                    try? await Task.sleep(for: backoff.delay(forAttempt: attempt))
                }
                continuation.finish()
            }
            continuation.onTermination = { _ in task.cancel() }
        }
    }

    /// Runs one connection to completion. Returns whether the server's
    /// opening comment was observed (controls the `.disconnected` signal —
    /// a connect that never opened stays silent so consumers don't see
    /// phantom connect/disconnect cycles).
    private static func runConnection(
        url: URL,
        headers: [String: String],
        configuration: Configuration,
        connector: @escaping Connector,
        continuation: AsyncStream<Event>.Continuation
    ) async -> Bool {
        let watchdog = Watchdog()
        // The reader and the watchdog race: whichever finishes first — byte
        // stream ending / silence expiring — cancels the other.
        await withTaskGroup(of: Void.self) { group in
            group.addTask {
                do {
                    let (statusCode, chunks) = try await connector(url, headers)
                    guard statusCode == 200 else { return }
                    var parser = SSEParser()
                    for try await chunk in chunks {
                        await watchdog.touch()
                        for item in parser.consume(chunk) {
                            await handle(item, watchdog: watchdog, continuation: continuation)
                        }
                    }
                } catch {
                    // Connect failures and mid-stream errors land in the
                    // shared reconnect path; there is nothing to distinguish.
                }
            }
            group.addTask {
                await watchdog.touch()
                while await watchdog.sleepUntilDeadline(configuration.silenceTimeout) {}
            }
            // First side to finish decides; tear the other down.
            await group.next()
            group.cancelAll()
        }
        return await watchdog.sawConnected
    }

    private static func handle(
        _ item: SSEParser.Item,
        watchdog: Watchdog,
        continuation: AsyncStream<Event>.Continuation
    ) async {
        switch item {
        case .comment("connected"):
            await watchdog.markConnected()
            continuation.yield(.connected)
        case .comment:
            // Heartbeats (and any future comments) only feed the watchdog.
            break
        case .data(let payload):
            guard let change = try? JSONDecoder().decode(Change.self, from: Data(payload.utf8)) else {
                return
            }
            continuation.yield(.change(change))
        }
    }

    private static func liveConnector(configuration: Configuration) -> Connector {
        { url, headers in
            // A dedicated ephemeral session whose request timeout sits above
            // the watchdog: URLSession's idle timeout must never win the
            // race, or reconnects would look like silent server deaths.
            let sessionConfiguration = URLSessionConfiguration.ephemeral
            sessionConfiguration.timeoutIntervalForRequest =
                Double(configuration.silenceTimeout.components.seconds) + 30
            let session = URLSession(configuration: sessionConfiguration)
            let bytes: URLSession.AsyncBytes
            let response: URLResponse
            var request = URLRequest(url: url)
            request.setValue("text/event-stream", forHTTPHeaderField: "Accept")
            for (header, value) in headers {
                request.setValue(value, forHTTPHeaderField: header)
            }
            do {
                (bytes, response) = try await session.bytes(for: request)
            } catch {
                // Sessions leak unless invalidated; every exit owns its own.
                session.invalidateAndCancel()
                throw error
            }
            let statusCode = (response as? HTTPURLResponse)?.statusCode ?? 0
            guard statusCode == 200 else {
                // Don't hand back a stream nobody will read — an error body
                // could otherwise buffer unbounded on a still-live session.
                session.invalidateAndCancel()
                return (statusCode, AsyncThrowingStream { $0.finish() })
            }
            let chunks = AsyncThrowingStream<[UInt8], any Error> { continuation in
                let reader = Task {
                    do {
                        // Byte-at-a-time is fine here: events are tiny and
                        // rare, and AsyncBytes buffers internally.
                        for try await byte in bytes {
                            continuation.yield([byte])
                        }
                        continuation.finish()
                    } catch {
                        continuation.finish(throwing: error)
                    }
                    session.invalidateAndCancel()
                }
                continuation.onTermination = { _ in reader.cancel() }
            }
            return (statusCode, chunks)
        }
    }
}

/// Shared per-connection state: the silence deadline (`touch` on every
/// chunk; `sleepUntilDeadline` returns false once a full quiet interval has
/// elapsed) and whether the server's opening comment was seen.
private actor Watchdog {
    private var last = ContinuousClock.now
    private(set) var sawConnected = false

    func touch() {
        last = ContinuousClock.now
    }

    func markConnected() {
        sawConnected = true
    }

    func sleepUntilDeadline(_ timeout: Duration) async -> Bool {
        let deadline = last + timeout
        let now = ContinuousClock.now
        if now >= deadline { return false }
        try? await Task.sleep(until: deadline)
        return !Task.isCancelled
    }
}

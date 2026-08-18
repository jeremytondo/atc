// SSEStream: the reconnecting text/event-stream engine behind every App
// Server SSE consumer (`ResourceEventStream`, `ThreadEventStream`). The
// public streams own only what differs — the URL, the payload type, and the
// meaning of a reconnect; everything below is shared so no consumer can get
// the framing invariants wrong:
// - `.connected` is signalled only after the server's opening `: connected`
//   comment — the server guarantees a subscriber registered before that byte,
//   so a consumer that (re)fetches state on `.connected` cannot miss a change.
// - Heartbeat comments arrive every ~25 s; a stream silent well past that is
//   dead. The watchdog tears it down and reconnects with backoff. Backoff
//   only resets once a connection has proven stable — a server that greets
//   and immediately drops must not induce a hot reconnect loop, because every
//   subscribe does work server-side.
// - The request URL is rebuilt on every attempt, so a consumer that resumes
//   (`after=seq`) always reconnects from where it actually is.
// - Payloads that fail to decode are dropped, never fatal (a decode failure
//   means contract drift, which regeneration catches).
//
// A stream retries forever; it ends only when the consuming task is
// cancelled. `.disconnected` marks each loss so consumers can surface
// reachability, but reachability policy lives above this seam.

import Foundation

/// Timing shared by every SSE stream; each public stream re-exports it as
/// its own `Configuration`.
public struct EventStreamConfiguration: Sendable {
    /// Heartbeats arrive every ~25 s; 60 s of silence means dead.
    public var silenceTimeout: Duration = .seconds(60)
    public var reconnectBaseDelay: Duration = .milliseconds(500)
    public var reconnectMaxDelay: Duration = .seconds(8)

    public init() {}
}

enum SSEStream {
    /// A single connection attempt: HTTP status plus the raw byte chunks.
    /// Injected in tests; the live path wraps URLSession.bytes.
    typealias Connector =
        @Sendable (URL, [String: String]) async throws
        -> (statusCode: Int, chunks: AsyncThrowingStream<[UInt8], any Error>)

    /// What one connection produces, before the consumer gives it meaning.
    enum Signal: Sendable, Equatable {
        /// The opening `: connected` comment arrived.
        case connected
        /// One dispatched event's data payload.
        case data(String)
        /// A connection that had opened was lost; a reconnect follows.
        case disconnected
    }

    /// Runs the reconnect loop, mapping each signal into the consumer's event
    /// type (`nil` drops the signal). `request` is evaluated per attempt.
    static func stream<Event: Sendable>(
        request: @escaping @Sendable () -> URL,
        headers: [String: String],
        configuration: EventStreamConfiguration,
        connector: @escaping Connector,
        map: @escaping @Sendable (Signal) -> Event?
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
                        url: request(),
                        headers: headers,
                        configuration: configuration,
                        connector: connector
                    ) { signal in
                        if let event = map(signal) { continuation.yield(event) }
                    }
                    if Task.isCancelled { break }
                    if sawConnected, let event = map(.disconnected) {
                        continuation.yield(event)
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
        configuration: EventStreamConfiguration,
        connector: @escaping Connector,
        emit: @escaping @Sendable (Signal) -> Void
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
                            switch item {
                            case .comment("connected"):
                                await watchdog.markConnected()
                                emit(.connected)
                            case .comment:
                                // Heartbeats (and any future comments) only
                                // feed the watchdog.
                                break
                            case .data(let payload):
                                emit(.data(payload))
                            }
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

    static func liveConnector(configuration: EventStreamConfiguration) -> Connector {
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
                        // Byte-at-a-time is fine here: events are small and
                        // AsyncBytes buffers internally.
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

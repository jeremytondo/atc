import Foundation

@testable import ATCAppServerTransport

/// Suspends until `condition` holds, giving the transport's own tasks room to
/// run. `attempts` is a failure bound, never the thing under test: every
/// caller waits on a real observable condition, and a passing test returns as
/// soon as it holds.
func waitUntil(
    attempts: Int = 2_000,
    _ condition: () -> Bool
) async -> Bool {
    for _ in 0..<attempts {
        if condition() { return true }
        try? await Task.sleep(for: .milliseconds(1))
    }
    return condition()
}

/// Scripted stand-in for the live URLSession connector shared by the SSE
/// stream tests: each connection attempt pops the next script; an exhausted
/// script hangs (a quiet, open stream) so a test never spins through
/// unplanned reconnects. Every attempt's URL and headers are recorded.
final class ScriptedConnector: @unchecked Sendable {
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
    private var recordedURLs: [URL] = []
    private var recordedHeaders: [String: String] = [:]
    private var openConnection: AsyncThrowingStream<[UInt8], any Error>.Continuation?

    var attempts: Int { lock.withLock { recordedURLs.count } }
    var urls: [URL] { lock.withLock { recordedURLs } }
    var headers: [String: String] { lock.withLock { recordedHeaders } }

    init(_ script: [Connection]) {
        self.script = script
    }

    /// Ends the connection currently held open, as a server dropping it
    /// would — for tests that must sequence the drop after the consumer has
    /// reacted to what was served.
    func endOpenConnection() {
        lock.withLock { openConnection }?.finish()
    }

    private func next(url: URL, headers: [String: String]) -> Connection {
        lock.lock()
        defer { lock.unlock() }
        recordedURLs.append(url)
        recordedHeaders = headers
        return script.isEmpty ? .stream([], thenHang: true) : script.removeFirst()
    }

    func connect() -> SSEStream.Connector {
        { url, headers in
            switch self.next(url: url, headers: headers) {
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
                        if thenHang {
                            self.lock.withLock { self.openConnection = continuation }
                        } else {
                            continuation.finish()
                        }
                    }
                )
            }
        }
    }
}

/// Consumes until `count` events arrive, calling `onEach` as they do; a
/// deadline turns a hung stream into a visible assertion failure instead of
/// a stuck suite.
func collect<Event: Sendable>(
    _ stream: AsyncStream<Event>,
    count: Int,
    onEach: @escaping @Sendable (Event) -> Void = { _ in }
) async -> [Event] {
    let collector = Task {
        var events: [Event] = []
        for await event in stream {
            events.append(event)
            onEach(event)
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

/// Stream timing tuned for tests: reconnects in a millisecond or two, and a
/// silence timeout long enough that a hung script does not fire it.
func fastConfiguration(silenceTimeout: Duration = .seconds(5)) -> EventStreamConfiguration {
    var configuration = EventStreamConfiguration()
    configuration.silenceTimeout = silenceTimeout
    configuration.reconnectBaseDelay = .milliseconds(1)
    configuration.reconnectMaxDelay = .milliseconds(5)
    return configuration
}

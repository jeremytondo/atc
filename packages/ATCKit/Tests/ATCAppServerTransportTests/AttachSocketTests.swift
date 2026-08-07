import Foundation
import Testing
@testable import ATCAppServerTransport

/// Scripted stand-in for one WebSocket connection: the test delivers server
/// frames and observes what the socket sends, cancels, and tears down.
private final class FakeWebSocket: AttachWebSocketTask, @unchecked Sendable {
    enum Sent: Equatable {
        case data(Data)
        case text(String)
    }

    private let lock = NSLock()
    private var incomingIterator: AsyncStream<URLSessionWebSocketTask.Message>.Iterator
    private let incoming: AsyncStream<URLSessionWebSocketTask.Message>.Continuation
    private var recordedSent: [Sent] = []
    private var recordedCancel: (code: URLSessionWebSocketTask.CloseCode, reason: String?)?
    private var recordedInvalidated = false
    private var openHandler: (@Sendable () -> Void)?
    private var storedCloseCode: URLSessionWebSocketTask.CloseCode = .invalid
    private var storedCloseReason: Data?
    private var storedStatusCode: Int?

    init() {
        var continuation: AsyncStream<URLSessionWebSocketTask.Message>.Continuation!
        let stream = AsyncStream<URLSessionWebSocketTask.Message> { continuation = $0 }
        incoming = continuation
        incomingIterator = stream.makeAsyncIterator()
    }

    // MARK: - Test controls

    func transport() -> AttachTransport {
        { _, onOpen in
            self.lock.withLock { self.openHandler = onOpen }
            return self
        }
    }

    func open() {
        lock.withLock { openHandler }?()
    }

    func deliver(_ message: URLSessionWebSocketTask.Message) {
        incoming.yield(message)
    }

    /// Server-side close: sets the close vocabulary, then ends receive().
    func closeFromServer(code: URLSessionWebSocketTask.CloseCode, reason: String?) {
        lock.withLock {
            storedCloseCode = code
            storedCloseReason = reason.map { Data($0.utf8) }
        }
        incoming.finish()
    }

    var sent: [Sent] { lock.withLock { recordedSent } }
    var cancelled: (code: URLSessionWebSocketTask.CloseCode, reason: String?)? {
        lock.withLock { recordedCancel }
    }
    var invalidated: Bool { lock.withLock { recordedInvalidated } }

    // MARK: - AttachWebSocketTask

    func receive() async throws -> URLSessionWebSocketTask.Message {
        if let message = await incomingIterator.next() {
            return message
        }
        throw URLError(.networkConnectionLost)
    }

    func send(_ message: URLSessionWebSocketTask.Message) async throws {
        lock.withLock {
            switch message {
            case .data(let data): recordedSent.append(.data(data))
            case .string(let text): recordedSent.append(.text(text))
            @unknown default: break
            }
        }
    }

    func sendKeepalivePing() {}

    func cancel(with closeCode: URLSessionWebSocketTask.CloseCode, reason: Data?) {
        lock.withLock {
            recordedCancel = (closeCode, reason.flatMap { String(data: $0, encoding: .utf8) })
        }
        incoming.finish()
    }

    func invalidate() {
        lock.withLock { recordedInvalidated = true }
    }

    var closeCode: URLSessionWebSocketTask.CloseCode { lock.withLock { storedCloseCode } }
    var closeReason: Data? { lock.withLock { storedCloseReason } }
    var httpStatusCode: Int? { lock.withLock { storedStatusCode } }
}

private let attachURL = URL(string: "ws://127.0.0.1:7332/api/v1/terminals/t1/attach")!

/// Collects socket events on a background task so tests wait on real
/// conditions (`waitUntil` over the captured list), never on fixed sleeps.
private final class EventLog: @unchecked Sendable {
    private let lock = NSLock()
    private var recorded: [AttachEvent] = []
    private var task: Task<Void, Never>?

    func observe(_ stream: AsyncStream<AttachEvent>) {
        task = Task {
            for await event in stream {
                lock.withLock { recorded.append(event) }
            }
        }
    }

    var events: [AttachEvent] { lock.withLock { recorded } }

    var lastEnd: AttachEndReason? {
        for event in events.reversed() {
            if case .ended(let reason) = event { return reason }
        }
        return nil
    }

    func cancel() {
        task?.cancel()
    }
}

private func waitUntil(
    attempts: Int = 2_000,
    _ condition: () -> Bool
) async -> Bool {
    for _ in 0..<attempts {
        if condition() { return true }
        try? await Task.sleep(for: .milliseconds(1))
    }
    return condition()
}

@Suite("Attach socket")
struct AttachSocketTests {
    @Test("server pings are answered with pongs, never echoed as output")
    func pingPong() async {
        let fake = FakeWebSocket()
        let socket = AttachSocket(url: attachURL, headers: [:], transport: fake.transport())
        let log = EventLog()
        log.observe(await socket.start())

        fake.open()
        fake.deliver(.string(#"{"type":"ping"}"#))

        let ponged = await waitUntil { fake.sent.contains(.text(#"{"type":"pong"}"#)) }
        #expect(ponged, "the pong reply is load-bearing: silent clients are closed after 120s")
        // The ping text frame must not surface as terminal output.
        #expect(
            !log.events.contains { event in
                if case .output = event { return true }
                return false
            }
        )
        await socket.close()
        log.cancel()
    }

    @Test("binary frames pass through both directions")
    func binaryPassthrough() async {
        let fake = FakeWebSocket()
        let socket = AttachSocket(url: attachURL, headers: [:], transport: fake.transport())
        let log = EventLog()
        log.observe(await socket.start())

        fake.open()
        fake.deliver(.data(Data("output".utf8)))
        socket.enqueue(Data("keys".utf8))

        let delivered = await waitUntil {
            log.events.contains { event in
                if case .output(let data) = event { return data == Data("output".utf8) }
                return false
            }
        }
        let forwarded = await waitUntil { fake.sent.contains(.data(Data("keys".utf8))) }
        #expect(delivered)
        #expect(forwarded)
        await socket.close()
        log.cancel()
    }

    @Test("resize is clamped and sent as a control frame")
    func resizeClamped() async {
        let fake = FakeWebSocket()
        let socket = AttachSocket(url: attachURL, headers: [:], transport: fake.transport())
        let log = EventLog()
        log.observe(await socket.start())

        fake.open()
        socket.enqueueResize(cols: 5000, rows: 0)

        let resized = await waitUntil {
            fake.sent.contains(.text(#"{"type":"resize","cols":1000,"rows":1}"#))
        }
        #expect(resized)
        await socket.close()
        log.cancel()
    }

    @Test("a server terminal_ended close is authoritative and tears down")
    func terminalEndedClose() async {
        let fake = FakeWebSocket()
        let socket = AttachSocket(url: attachURL, headers: [:], transport: fake.transport())
        let log = EventLog()
        log.observe(await socket.start())

        fake.open()
        fake.closeFromServer(code: .normalClosure, reason: "terminal_ended")

        let ended = await waitUntil { log.lastEnd == .terminalEnded }
        #expect(ended)
        #expect(await waitUntil { fake.invalidated })
        log.cancel()
    }

    @Test("dropping the event stream detaches instead of leaking the socket")
    func consumerCancellationCloses() async {
        let fake = FakeWebSocket()
        let socket = AttachSocket(url: attachURL, headers: [:], transport: fake.transport())
        let log = EventLog()
        log.observe(await socket.start())

        fake.open()
        // The consumer walks away without calling close() — an abandoned
        // socket would keep answering pings and never be reaped serverside.
        log.cancel()

        let detached = await waitUntil {
            fake.cancelled?.code == .normalClosure && fake.cancelled?.reason == "detach"
        }
        #expect(detached)
        #expect(await waitUntil { fake.invalidated })
    }

    @Test("close() sends the protocol's detach close")
    func explicitDetach() async {
        let fake = FakeWebSocket()
        let socket = AttachSocket(url: attachURL, headers: [:], transport: fake.transport())
        let log = EventLog()
        log.observe(await socket.start())

        fake.open()
        await socket.close()

        #expect(fake.cancelled?.code == .normalClosure)
        #expect(fake.cancelled?.reason == "detach")
        let ended = await waitUntil { log.lastEnd == .closedByClient }
        #expect(ended)
        log.cancel()
    }
}

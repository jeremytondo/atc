// AttachSocket: one WebSocket attach to the App Server's
// `GET /api/v1/terminals/{terminalId}/attach`.
//
// Framing per AttachProtocol: binary frames are raw terminal bytes both
// directions; text frames are JSON control messages (the server pings every
// 30 s and closes clients silent for 120 s, so the pong reply here is
// load-bearing). Unknown or malformed text frames are ignored, never fatal
// and never written to the terminal. One instance per connection attempt —
// reconnect makes a new one. Only `terminalEnded` proves the terminal is
// gone; every retryable end must be followed by a reconciled
// `GET /terminals/{id}` before reattaching.

import Foundation

/// Why the attach stream stopped.
public enum AttachEndReason: Sendable, Equatable {
    /// Authoritative: `1000 terminal_ended`, or pre-upgrade 410 (tombstone).
    /// The terminal is gone; do not reattach.
    case terminalEnded
    /// Pre-upgrade HTTP rejection other than 410 (404 unknown terminal,
    /// 426 upgrade misuse). Refetch state; do not blindly retry.
    case rejected(statusCode: Int)
    /// Says nothing about the terminal: retryable server closes (1011),
    /// abnormal closure (1006), or transport failure. Refetch the terminal
    /// and reattach if it is still live.
    case retryable(String)
    /// We hung up (detach / app teardown).
    case closedByClient
}

public enum AttachEvent: Sendable {
    case connected
    case output(Data)
    case ended(AttachEndReason)
}

/// Seam over one WebSocket connection so the socket's protocol behavior —
/// the pong reply, binary passthrough, close mapping, teardown — is testable
/// without a live server. The live implementation wraps URLSession.
protocol AttachWebSocketTask: Sendable {
    func receive() async throws -> URLSessionWebSocketTask.Message
    func send(_ message: URLSessionWebSocketTask.Message) async throws
    /// Best-effort protocol-level keepalive; failures are not a disconnect
    /// signal (the receive loop owns failure detection).
    func sendKeepalivePing()
    func cancel(with closeCode: URLSessionWebSocketTask.CloseCode, reason: Data?)
    /// Releases the connection's owning resources once the task is done.
    func invalidate()
    var closeCode: URLSessionWebSocketTask.CloseCode { get }
    var closeReason: Data? { get }
    /// Pre-upgrade HTTP status when the server rejected the handshake
    /// (a failed handshake is a plain HTTP response, never a close frame).
    var httpStatusCode: Int? { get }
}

/// Opens one connection; `onOpen` fires when the handshake completes.
typealias AttachTransport =
    @Sendable (
        _ request: URLRequest,
        _ onOpen: @escaping @Sendable () -> Void
    ) -> any AttachWebSocketTask

public actor AttachSocket {
    /// Protocol-level pings are a liveness backstop for sleeping Macs and
    /// idle tunnel forwards. They are independent of the server's app-level
    /// text-frame pings, which Bun neither sends nor surfaces.
    private static let pingInterval: Duration = .seconds(10)

    private let request: URLRequest
    private let transport: AttachTransport
    private var socket: (any AttachWebSocketTask)?
    private var pingTask: Task<Void, Never>?
    private var pumpTask: Task<Void, Never>?
    private var closedByClient = false
    private var finished = false

    private let outbound = OutboundQueue()
    /// Wake-up for the pump; the data itself lives in `outbound`, so one
    /// buffered signal is always enough.
    private let outboundSignal: AsyncStream<Void>
    private let outboundSignalContinuation: AsyncStream<Void>.Continuation

    public init(url: URL, headers: [String: String] = [:]) {
        self.init(url: url, headers: headers, transport: LiveWebSocketTask.transport)
    }

    init(url: URL, headers: [String: String], transport: @escaping AttachTransport) {
        var request = URLRequest(url: url)
        for (header, value) in headers {
            request.setValue(value, forHTTPHeaderField: header)
        }
        self.request = request
        self.transport = transport
        (outboundSignal, outboundSignalContinuation) = AsyncStream.makeStream(
            of: Void.self, bufferingPolicy: .bufferingNewest(1)
        )
    }

    // MARK: - Producer side (called synchronously from the terminal's
    // background callbacks; the lock-guarded queue keeps keystroke ordering)

    /// May block the calling thread for backpressure when the outbound
    /// buffer is full — call from the terminal's background write callback,
    /// never from the main actor or a cooperative-pool task.
    public nonisolated func enqueue(_ data: Data) {
        outbound.enqueue(data) {
            // Large writes may block for backpressure and enter the queue in
            // several chunks. Wake the pump after every chunk so it can make
            // room for the remainder of the same synchronous callback.
            outboundSignalContinuation.yield(())
        }
    }

    public nonisolated func enqueueResize(cols: Int, rows: Int) {
        outbound.setResize(
            cols: AttachProtocol.clampDimension(Double(cols), fallback: AttachProtocol.fallbackCols),
            rows: AttachProtocol.clampDimension(Double(rows), fallback: AttachProtocol.fallbackRows)
        )
        outboundSignalContinuation.yield(())
    }

    // MARK: - Lifecycle

    /// Opens the socket and returns the event stream. Call once. Dropping
    /// the stream (consumer cancellation) closes the socket: an abandoned
    /// attach would otherwise keep answering server pings indefinitely.
    public func start() -> AsyncStream<AttachEvent> {
        AsyncStream { continuation in
            let socket = transport(request) {
                continuation.yield(.connected)
            }
            self.socket = socket

            continuation.onTermination = { [weak self] termination in
                // finish() ends the stream itself; only consumer
                // cancellation needs the teardown driven from here.
                guard case .cancelled = termination else { return }
                Task { await self?.close() }
            }

            startOutboundPump(socket)
            startPingLoop(socket)

            Task { await self.receiveLoop(socket, continuation: continuation) }
        }
    }

    /// Deliberate detach: the session keeps running server-side.
    public func close() {
        closedByClient = true
        socket?.cancel(with: .normalClosure, reason: Data(AttachProtocol.detachReason.utf8))
        tearDown()
    }

    private func tearDown() {
        pingTask?.cancel()
        pumpTask?.cancel()
        // Cancelled pumps cannot make more queue space. Finish the queue to
        // wake any producer currently waiting on backpressure.
        outbound.finish()
        outboundSignalContinuation.finish()
        socket?.invalidate()
        socket = nil
    }

    // MARK: - Loops

    private func receiveLoop(
        _ socket: any AttachWebSocketTask,
        continuation: AsyncStream<AttachEvent>.Continuation
    ) async {
        while true {
            do {
                let message = try await socket.receive()
                switch message {
                case .data(let data):
                    continuation.yield(.output(data))
                case .string(let string):
                    handleControlFrame(string)
                @unknown default:
                    break
                }
            } catch {
                finish(continuation, reason: endReason(for: socket, error: error))
                return
            }
        }
    }

    private func handleControlFrame(_ text: String) {
        // Text frames are control messages, never terminal output. The only
        // one the server sends today is the keepalive ping.
        guard case .ping = AttachProtocol.ControlFrame.decode(text) else { return }
        outbound.setPong()
        outboundSignalContinuation.yield(())
    }

    private func finish(_ continuation: AsyncStream<AttachEvent>.Continuation, reason: AttachEndReason) {
        guard !finished else { return }
        finished = true
        continuation.yield(.ended(reason))
        continuation.finish()
        tearDown()
    }

    private func endReason(for socket: any AttachWebSocketTask, error: any Error) -> AttachEndReason {
        Self.endReason(
            closedByClient: closedByClient,
            statusCode: socket.httpStatusCode,
            closeCode: socket.closeCode,
            closeReason: socket.closeReason,
            errorDescription: error.localizedDescription
        )
    }

    static func endReason(
        closedByClient: Bool = false,
        statusCode: Int? = nil,
        closeCode: URLSessionWebSocketTask.CloseCode,
        closeReason: Data?,
        errorDescription: String
    ) -> AttachEndReason {
        if closedByClient {
            return .closedByClient
        }
        if let statusCode, statusCode != 101 {
            return statusCode == 410 ? .terminalEnded : .rejected(statusCode: statusCode)
        }
        let reason = closeReason.flatMap { String(data: $0, encoding: .utf8) }
        switch AttachProtocol.classifyClose(code: closeCode.rawValue, reason: reason) {
        case .terminalEnded:
            return .terminalEnded
        case .detach:
            // Only this client sends detach; receiving it back means we
            // initiated the close but the flag lost the race.
            return .closedByClient
        case .retryable:
            return .retryable(reason ?? errorDescription)
        }
    }

    private func startOutboundPump(_ socket: any AttachWebSocketTask) {
        pumpTask = Task {
            for await _ in outboundSignal {
                while let item = outbound.dequeue() {
                    do {
                        switch item {
                        case .data(let data):
                            try await socket.send(.data(data))
                        case .resize(let cols, let rows):
                            try await socket.send(
                                .string(AttachProtocol.ControlFrame.resize(cols: cols, rows: rows).wire)
                            )
                        case .pong:
                            try await socket.send(.string(AttachProtocol.ControlFrame.pong.wire))
                        }
                    } catch {
                        // The receive loop surfaces the transport failure.
                        // Stop accepting input immediately so a producer
                        // cannot remain blocked behind this dead pump.
                        outbound.finish()
                        return
                    }
                }
            }
        }
    }

    /// URLSessionWebSocketTask does not auto-ping. Without this, dead peers
    /// linger and idle SSH-tunnel forwards get reaped.
    private func startPingLoop(_ socket: any AttachWebSocketTask) {
        pingTask = Task {
            while !Task.isCancelled {
                try? await Task.sleep(for: Self.pingInterval)
                guard !Task.isCancelled else { return }
                socket.sendKeepalivePing()
            }
        }
    }
}

/// The live transport: one URLSession per connection, torn down with it.
private final class LiveWebSocketTask: AttachWebSocketTask, @unchecked Sendable {
    static let transport: AttachTransport = { request, onOpen in
        LiveWebSocketTask(request: request, onOpen: onOpen)
    }

    private let session: URLSession
    private let task: URLSessionWebSocketTask

    init(request: URLRequest, onOpen: @escaping @Sendable () -> Void) {
        session = URLSession(
            configuration: .default,
            delegate: SocketDelegate(onOpen: onOpen),
            delegateQueue: nil
        )
        task = session.webSocketTask(with: request)
        task.maximumMessageSize = 1 << 20
        task.resume()
    }

    func receive() async throws -> URLSessionWebSocketTask.Message {
        try await task.receive()
    }

    func send(_ message: URLSessionWebSocketTask.Message) async throws {
        try await task.send(message)
    }

    func sendKeepalivePing() {
        task.sendPing { _ in
            // Best effort: pings keep tunnels and NATs from reaping an idle
            // connection and give the OS traffic with which to notice a
            // dead peer.
        }
    }

    func cancel(with closeCode: URLSessionWebSocketTask.CloseCode, reason: Data?) {
        task.cancel(with: closeCode, reason: reason)
    }

    func invalidate() {
        session.finishTasksAndInvalidate()
    }

    var closeCode: URLSessionWebSocketTask.CloseCode { task.closeCode }
    var closeReason: Data? { task.closeReason }
    var httpStatusCode: Int? { (task.response as? HTTPURLResponse)?.statusCode }
}

/// Delegate that surfaces the WebSocket handshake completing. Receive-path
/// errors and close codes are still read off the task itself.
private final class SocketDelegate: NSObject, URLSessionWebSocketDelegate {
    private let onOpen: @Sendable () -> Void

    init(onOpen: @escaping @Sendable () -> Void) {
        self.onOpen = onOpen
    }

    func urlSession(
        _ session: URLSession,
        webSocketTask: URLSessionWebSocketTask,
        didOpenWithProtocol protocol: String?
    ) {
        onOpen()
    }
}

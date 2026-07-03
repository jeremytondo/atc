import Foundation

/// Why the attach stream stopped.
enum AttachEndReason: Sendable, Equatable {
    /// Server close 1000 — the underlying session ended.
    case sessionEnded
    /// Server close 1011.
    case serverError
    /// Network drop or handshake failure.
    case transportFailure(String)
    /// We hung up (Disconnect button / app teardown).
    case closedByClient
}

enum AttachEvent: Sendable {
    case connected
    case output(Data)
    case ended(AttachEndReason)
}

/// One WebSocket attach to `GET /api/sessions/{id}/attach`.
///
/// Protocol: server→client binary = terminal output; client→server binary =
/// keystrokes; client→server TEXT = `{"type":"resize","cols":N,"rows":N}`.
/// One instance per connection attempt — reconnect makes a new one.
actor AttachConnection {
    private enum Outbound {
        case data(Data)
        case resize(cols: UInt16, rows: UInt16)
    }

    /// Server frame read limit is 1 MiB; stay well under it.
    private static let chunkSize = 256 * 1024
    private static let pingInterval: Duration = .seconds(30)

    private let request: URLRequest
    private let socketDelegate: SocketDelegate
    private let urlSession: URLSession
    private var task: URLSessionWebSocketTask?
    private var pingTask: Task<Void, Never>?
    private var pumpTask: Task<Void, Never>?
    private var closedByClient = false
    private var finished = false

    private let outbound: AsyncStream<Outbound>
    private let outboundContinuation: AsyncStream<Outbound>.Continuation

    init(url: URL, headers: [String: String]) {
        var request = URLRequest(url: url)
        for (header, value) in headers {
            request.setValue(value, forHTTPHeaderField: header)
        }
        self.request = request
        socketDelegate = SocketDelegate()
        urlSession = URLSession(configuration: .default, delegate: socketDelegate, delegateQueue: nil)
        (outbound, outboundContinuation) = AsyncStream.makeStream(of: Outbound.self)
    }

    // MARK: - Producer side (called synchronously from Ghostty callbacks;
    // a single AsyncStream keeps keystroke/resize ordering intact)

    nonisolated func enqueue(_ data: Data) {
        outboundContinuation.yield(.data(data))
    }

    nonisolated func enqueueResize(cols: UInt16, rows: UInt16) {
        outboundContinuation.yield(.resize(cols: cols, rows: rows))
    }

    // MARK: - Lifecycle

    /// Opens the socket and returns the event stream. Call once.
    func start() -> AsyncStream<AttachEvent> {
        AsyncStream { continuation in
            let task = urlSession.webSocketTask(with: request)
            task.maximumMessageSize = 1 << 20
            self.task = task

            // The delegate's didOpen is the only reliable "connected"
            // signal (ping completions can be silently dropped during a
            // failed handshake).
            socketDelegate.onOpen = {
                continuation.yield(.connected)
            }

            task.resume()

            startOutboundPump(task)
            startPingLoop(task)

            Task { await self.receiveLoop(task, continuation: continuation) }
        }
    }

    func close() {
        closedByClient = true
        task?.cancel(with: .normalClosure, reason: nil)
        tearDown()
    }

    private func tearDown() {
        pingTask?.cancel()
        pumpTask?.cancel()
        outboundContinuation.finish()
        urlSession.finishTasksAndInvalidate()
    }

    // MARK: - Loops

    private func receiveLoop(
        _ task: URLSessionWebSocketTask,
        continuation: AsyncStream<AttachEvent>.Continuation
    ) async {
        while true {
            do {
                let message = try await task.receive()
                switch message {
                case .data(let data):
                    continuation.yield(.output(data))
                case .string(let string):
                    // The server only sends binary; tolerate text anyway.
                    continuation.yield(.output(Data(string.utf8)))
                @unknown default:
                    break
                }
            } catch {
                finish(continuation, reason: endReason(for: task, error: error))
                return
            }
        }
    }

    private func finish(_ continuation: AsyncStream<AttachEvent>.Continuation, reason: AttachEndReason) {
        guard !finished else { return }
        finished = true
        continuation.yield(.ended(reason))
        continuation.finish()
        tearDown()
    }

    private func endReason(for task: URLSessionWebSocketTask, error: any Error) -> AttachEndReason {
        if closedByClient {
            return .closedByClient
        }
        switch task.closeCode {
        case .normalClosure:
            return .sessionEnded
        case .internalServerError:
            return .serverError
        default:
            return .transportFailure(error.localizedDescription)
        }
    }

    private func startOutboundPump(_ task: URLSessionWebSocketTask) {
        pumpTask = Task {
            for await item in outbound {
                do {
                    switch item {
                    case .data(let data):
                        for chunk in Self.chunked(data) {
                            try await task.send(.data(chunk))
                        }
                    case .resize(let cols, let rows):
                        try await task.send(.string(#"{"type":"resize","cols":\#(cols),"rows":\#(rows)}"#))
                    }
                } catch {
                    // The receive loop surfaces the failure; just stop pumping.
                    return
                }
            }
        }
    }

    /// URLSessionWebSocketTask does not auto-ping. Without this, dead peers
    /// linger and idle SSH-tunnel forwards get reaped.
    private func startPingLoop(_ task: URLSessionWebSocketTask) {
        pingTask = Task {
            while !Task.isCancelled {
                try? await Task.sleep(for: Self.pingInterval)
                guard !Task.isCancelled else { return }
                task.sendPing { _ in
                    // A failed ping tears the connection; the receive loop
                    // reports it.
                }
            }
        }
    }

    private static func chunked(_ data: Data) -> [Data] {
        guard data.count > chunkSize else { return [data] }
        var chunks: [Data] = []
        var offset = data.startIndex
        while offset < data.endIndex {
            let end = data.index(offset, offsetBy: chunkSize, limitedBy: data.endIndex) ?? data.endIndex
            chunks.append(data.subdata(in: offset..<end))
            offset = end
        }
        return chunks
    }
}

/// Delegate that surfaces the WebSocket handshake completing. Receive-path
/// errors and close codes are still read off the task itself.
private final class SocketDelegate: NSObject, URLSessionWebSocketDelegate, @unchecked Sendable {
    var onOpen: (@Sendable () -> Void)?

    func urlSession(
        _ session: URLSession,
        webSocketTask: URLSessionWebSocketTask,
        didOpenWithProtocol protocol: String?
    ) {
        onOpen?()
    }
}

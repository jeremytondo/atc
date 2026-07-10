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

    private let outbound = OutboundQueue()
    /// Wake-up for the pump; the data itself lives in `outbound`, so one
    /// buffered signal is always enough.
    private let outboundSignal: AsyncStream<Void>
    private let outboundSignalContinuation: AsyncStream<Void>.Continuation

    init(url: URL, headers: [String: String]) {
        var request = URLRequest(url: url)
        for (header, value) in headers {
            request.setValue(value, forHTTPHeaderField: header)
        }
        self.request = request
        socketDelegate = SocketDelegate()
        urlSession = URLSession(configuration: .default, delegate: socketDelegate, delegateQueue: nil)
        (outboundSignal, outboundSignalContinuation) = AsyncStream.makeStream(
            of: Void.self, bufferingPolicy: .bufferingNewest(1)
        )
    }

    // MARK: - Producer side (called synchronously from Ghostty callbacks;
    // the lock-guarded queue keeps keystroke ordering intact)

    nonisolated func enqueue(_ data: Data) {
        outbound.enqueue(data)
        outboundSignalContinuation.yield(())
    }

    nonisolated func enqueueResize(cols: UInt16, rows: UInt16) {
        outbound.setResize(cols: cols, rows: rows)
        outboundSignalContinuation.yield(())
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
        outboundSignalContinuation.finish()
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
            for await _ in outboundSignal {
                while let item = outbound.dequeue() {
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

/// Lock-guarded outbound buffer. Keystroke chunks keep their order under a
/// byte bound — a stalled socket plus a huge paste drops the excess instead
/// of growing memory — and only the most recent resize is kept, sent ahead
/// of data so the server sizes the PTY before applying input.
private final class OutboundQueue: @unchecked Sendable {
    /// Matches the server's per-frame read limit; more than this buffered
    /// means the socket is effectively dead anyway.
    private static let maxBufferedBytes = 1 << 20

    private let lock = NSLock()
    private var chunks: [Data] = []
    private var bufferedBytes = 0
    private var pendingResize: (cols: UInt16, rows: UInt16)?

    enum Item {
        case data(Data)
        case resize(cols: UInt16, rows: UInt16)
    }

    func enqueue(_ data: Data) {
        lock.withLock {
            guard bufferedBytes + data.count <= Self.maxBufferedBytes else { return }
            chunks.append(data)
            bufferedBytes += data.count
        }
    }

    func setResize(cols: UInt16, rows: UInt16) {
        lock.withLock { pendingResize = (cols, rows) }
    }

    func dequeue() -> Item? {
        lock.withLock {
            if let resize = pendingResize {
                pendingResize = nil
                return .resize(cols: resize.cols, rows: resize.rows)
            }
            guard !chunks.isEmpty else { return nil }
            let data = chunks.removeFirst()
            bufferedBytes -= data.count
            return .data(data)
        }
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

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
    /// Frequent pings are a liveness backstop for sleeping Macs, network
    /// changes, and idle SSH-tunnel forwards.
    private static let pingInterval: Duration = .seconds(10)

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
        outbound.enqueue(data) {
            // Large writes may block for backpressure and enter the queue in
            // several chunks. Wake the pump after every chunk so it can make
            // room for the remainder of the same synchronous callback.
            outboundSignalContinuation.yield(())
        }
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
        // Cancelled pumps cannot make more queue space. Finish the queue to
        // wake any Ghostty callback currently waiting on backpressure.
        outbound.finish()
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
                            try await task.send(.data(data))
                        case .resize(let cols, let rows):
                            try await task.send(.string(#"{"type":"resize","cols":\#(cols),"rows":\#(rows)}"#))
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

}

/// Lock-guarded outbound buffer. Keystroke chunks keep their order under a
/// byte bound. A full queue applies synchronous backpressure to Ghostty's
/// background write callback instead of discarding paste data. Only the most
/// recent resize is kept, sent ahead of data so the server sizes the PTY
/// before applying input.
nonisolated final class OutboundQueue: @unchecked Sendable {
    /// The server's 1 MiB limit applies to individual WebSocket frames, not
    /// the aggregate backlog. Eight MiB accommodates paste-heavy workflows
    /// while still bounding a stalled connection's retained input.
    private static let defaultMaxBufferedBytes = 8 << 20
    /// Stay comfortably below the server's per-frame read limit.
    private static let defaultMaxChunkBytes = 256 << 10

    /// Serializes whole producer calls. Without this, two Ghostty callbacks
    /// could interleave when the first one waits for queue space.
    private let producerLock = NSLock()
    private let condition = NSCondition()
    private let maxBufferedBytes: Int
    private let maxChunkBytes: Int
    private var chunks: [Data] = []
    private var firstChunkIndex = 0
    private var bufferedBytes = 0
    private var pendingResize: (cols: UInt16, rows: UInt16)?
    private var isFinished = false

    enum Item {
        case data(Data)
        case resize(cols: UInt16, rows: UInt16)
    }

    init(
        maxBufferedBytes: Int = OutboundQueue.defaultMaxBufferedBytes,
        maxChunkBytes: Int = OutboundQueue.defaultMaxChunkBytes
    ) {
        precondition(maxBufferedBytes > 0)
        precondition(maxChunkBytes > 0)
        self.maxBufferedBytes = maxBufferedBytes
        self.maxChunkBytes = min(maxChunkBytes, maxBufferedBytes)
    }

    /// Returns false only when shutdown interrupts or precedes the write.
    /// `didEnqueue` must wake the consumer after each accepted chunk: a write
    /// larger than the byte bound cannot finish until the consumer drains it.
    @discardableResult
    func enqueue(_ data: Data, didEnqueue: @Sendable () -> Void = {}) -> Bool {
        producerLock.lock()
        defer { producerLock.unlock() }

        var offset = data.startIndex
        repeat {
            condition.lock()
            while !isFinished && bufferedBytes == maxBufferedBytes {
                condition.wait()
            }
            guard !isFinished else {
                condition.unlock()
                return false
            }
            guard offset < data.endIndex else {
                condition.unlock()
                return true
            }

            let availableBytes = maxBufferedBytes - bufferedBytes
            let chunkCount = min(maxChunkBytes, availableBytes, data.distance(from: offset, to: data.endIndex))
            let end = data.index(offset, offsetBy: chunkCount)
            let chunk = data.subdata(in: offset..<end)
            chunks.append(chunk)
            bufferedBytes += chunk.count
            offset = end
            condition.unlock()

            didEnqueue()
        } while offset < data.endIndex

        return true
    }

    func setResize(cols: UInt16, rows: UInt16) {
        condition.lock()
        if !isFinished {
            pendingResize = (cols, rows)
        }
        condition.unlock()
    }

    func dequeue() -> Item? {
        condition.lock()
        defer { condition.unlock() }

        if let resize = pendingResize {
            pendingResize = nil
            return .resize(cols: resize.cols, rows: resize.rows)
        }
        guard firstChunkIndex < chunks.count else { return nil }

        let data = chunks[firstChunkIndex]
        // Release the queue's reference immediately without paying the
        // removeFirst() copy on every keystroke.
        chunks[firstChunkIndex] = Data()
        firstChunkIndex += 1
        bufferedBytes -= data.count
        if firstChunkIndex == chunks.count {
            chunks.removeAll(keepingCapacity: true)
            firstChunkIndex = 0
        } else if firstChunkIndex >= 64, firstChunkIndex * 2 >= chunks.count {
            chunks.removeFirst(firstChunkIndex)
            firstChunkIndex = 0
        }
        condition.signal()
        return .data(data)
    }

    /// Discards input only as part of explicit connection teardown and wakes
    /// every producer that may be blocked waiting for the cancelled pump.
    func finish() {
        condition.lock()
        guard !isFinished else {
            condition.unlock()
            return
        }
        isFinished = true
        chunks.removeAll()
        firstChunkIndex = 0
        bufferedBytes = 0
        pendingResize = nil
        condition.broadcast()
        condition.unlock()
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

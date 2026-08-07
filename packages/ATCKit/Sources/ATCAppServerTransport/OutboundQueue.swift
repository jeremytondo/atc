// OutboundQueue: lock-guarded outbound buffer for one attach socket.
//
// Keystroke chunks keep their order under a byte bound. A full queue applies
// synchronous backpressure to the terminal's background write callback
// instead of discarding paste data. Control frames collapse: only the most
// recent resize and a single pending pong are kept, sent ahead of data so the
// server sizes the PTY (and observes liveness) before applying input.

import Foundation

final class OutboundQueue: @unchecked Sendable {
    /// The server's 1 MiB limit applies to individual WebSocket frames, not
    /// the aggregate backlog. Eight MiB accommodates paste-heavy workflows
    /// while still bounding a stalled connection's retained input.
    private static let defaultMaxBufferedBytes = 8 << 20
    /// Stay comfortably below the server's per-frame read limit.
    private static let defaultMaxChunkBytes = 256 << 10

    /// Serializes whole producer calls. Without this, two write callbacks
    /// could interleave when the first one waits for queue space.
    private let producerLock = NSLock()
    private let condition = NSCondition()
    private let maxBufferedBytes: Int
    private let maxChunkBytes: Int
    private var chunks: [Data] = []
    private var firstChunkIndex = 0
    private var bufferedBytes = 0
    private var pendingResize: (cols: Int, rows: Int)?
    private var pendingPong = false
    private var isFinished = false

    enum Item: Equatable {
        case data(Data)
        case resize(cols: Int, rows: Int)
        case pong
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

        // Nothing to buffer — don't park an empty write behind a full queue.
        if data.isEmpty {
            condition.lock()
            defer { condition.unlock() }
            return !isFinished
        }

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

    func setResize(cols: Int, rows: Int) {
        condition.lock()
        if !isFinished {
            pendingResize = (cols, rows)
        }
        condition.unlock()
    }

    func setPong() {
        condition.lock()
        if !isFinished {
            pendingPong = true
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
        if pendingPong {
            pendingPong = false
            return .pong
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
        pendingPong = false
        condition.broadcast()
        condition.unlock()
    }
}

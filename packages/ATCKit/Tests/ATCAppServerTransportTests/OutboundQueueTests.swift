import Foundation
import Testing

@testable import ATCAppServerTransport

@Suite("Attach outbound queue")
struct OutboundQueueTests {
    @Test("a paste larger than one MiB is queued intact")
    func largePasteIsLossless() {
        let queue = OutboundQueue()
        let paste = patternedData(count: (1 << 20) + 1_337)

        #expect(queue.enqueue(paste))

        var received = Data()
        while let item = queue.dequeue() {
            switch item {
            case .data(let data):
                // Each WebSocket message stays below the server's frame cap.
                #expect(data.count <= 256 << 10)
                received.append(data)
            case .resize, .pong:
                Issue.record("unexpected control item")
            }
        }

        #expect(received == paste)
    }

    @Test("control frames jump ahead of buffered data and collapse")
    func controlFramesLeapfrogData() {
        let queue = OutboundQueue()
        #expect(queue.enqueue(Data("keystrokes".utf8)))
        queue.setResize(cols: 80, rows: 24)
        queue.setResize(cols: 120, rows: 40)
        queue.setPong()
        queue.setPong()

        // Only the latest resize and a single pong survive, both ahead of
        // the data so the server sizes the PTY before applying input.
        #expect(queue.dequeue() == .resize(cols: 120, rows: 40))
        #expect(queue.dequeue() == .pong)
        #expect(queue.dequeue() == .data(Data("keystrokes".utf8)))
        #expect(queue.dequeue() == nil)
    }

    @Test("backpressure preserves whole-write ordering across producers")
    func backpressurePreservesProducerOrdering() async {
        let queue = OutboundQueue(maxBufferedBytes: 4, maxChunkBytes: 2)
        let firstProbe = ProducerProbe()
        let firstData = Data("AAAAAA".utf8)
        let secondData = Data("BBBB".utf8)

        let first = Task.detached {
            queue.enqueue(firstData) { firstProbe.recordEnqueuedChunk() }
        }

        // Two chunks fill the four-byte queue, leaving the first producer
        // blocked with two bytes still to write.
        let firstFilledQueue = await waitUntil { firstProbe.enqueuedChunks == 2 }
        guard firstFilledQueue else {
            queue.finish()
            _ = await first.value
            Issue.record("first producer never filled the queue")
            return
        }

        let second = Task.detached { queue.enqueue(secondData) }
        var received = Data()
        let receivedEverything = await waitUntil {
            while let item = queue.dequeue() {
                if case .data(let data) = item {
                    received.append(data)
                }
            }
            return received.count == firstData.count + secondData.count
        }

        if !receivedEverything {
            queue.finish()
        }
        let firstAccepted = await first.value
        let secondAccepted = await second.value

        #expect(receivedEverything)
        #expect(firstAccepted)
        #expect(secondAccepted)
        #expect(received == firstData + secondData)
    }

    @Test("an empty write returns immediately even when the queue is full")
    func emptyWriteNeverBlocks() async {
        let queue = OutboundQueue(maxBufferedBytes: 4, maxChunkBytes: 4)
        let probe = ProducerProbe()
        #expect(queue.enqueue(Data("full".utf8)))

        // Run detached so a regression shows up as a failed expectation
        // instead of hanging the suite on the blocked producer.
        let empty = Task.detached {
            let accepted = queue.enqueue(Data())
            probe.recordFinished(accepted: accepted)
            return accepted
        }
        let finished = await waitUntil { probe.accepted != nil }
        if !finished {
            queue.finish()
        }
        let accepted = await empty.value
        #expect(finished, "empty write blocked behind a full queue")
        #expect(accepted)

        // After shutdown even an empty write reports the queue as unusable.
        queue.finish()
        #expect(!queue.enqueue(Data()))
    }

    @Test("finish wakes a backpressured producer and rejects later writes")
    func finishWakesBlockedProducer() async {
        let queue = OutboundQueue(maxBufferedBytes: 4, maxChunkBytes: 4)
        let probe = ProducerProbe()
        #expect(queue.enqueue(Data("full".utf8)))

        let blocked = Task.detached {
            probe.recordStarted()
            let accepted = queue.enqueue(Data("x".utf8))
            probe.recordFinished(accepted: accepted)
            return accepted
        }

        // Wait on the producer actually running, then release it via finish.
        // Whether finish lands before or after the producer reaches its
        // condition wait, the write must be rejected and nothing may hang —
        // a lost wake-up would deadlock `blocked.value` and fail the suite.
        let producerStarted = await waitUntil { probe.started }
        #expect(producerStarted)

        queue.finish()
        let accepted = await blocked.value
        #expect(!accepted)
        #expect(probe.accepted == false)
        #expect(queue.dequeue() == nil)
        #expect(!queue.enqueue(Data("later".utf8)))
    }
}

private func patternedData(count: Int) -> Data {
    Data((0..<count).map { UInt8(truncatingIfNeeded: $0) })
}

nonisolated private final class ProducerProbe: @unchecked Sendable {
    private let lock = NSLock()
    private var storedStarted = false
    private var storedEnqueuedChunks = 0
    private var storedAccepted: Bool?

    var started: Bool { lock.withLock { storedStarted } }
    var enqueuedChunks: Int { lock.withLock { storedEnqueuedChunks } }
    var accepted: Bool? { lock.withLock { storedAccepted } }

    func recordStarted() {
        lock.withLock { storedStarted = true }
    }

    func recordEnqueuedChunk() {
        lock.withLock { storedEnqueuedChunks += 1 }
    }

    func recordFinished(accepted: Bool) {
        lock.withLock { storedAccepted = accepted }
    }
}

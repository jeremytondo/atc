import Foundation
import Testing
@testable import ATC

@Suite("Terminal outbound queue")
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
            case .resize:
                Issue.record("unexpected resize")
            }
        }

        #expect(received == paste)
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

        let producerStarted = await waitUntil { probe.started }
        // Give the synchronous enqueue enough time to reach its condition
        // wait. It must remain pending while the active queue is full.
        try? await Task.sleep(for: .milliseconds(20))
        #expect(producerStarted)
        #expect(probe.accepted == nil)

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

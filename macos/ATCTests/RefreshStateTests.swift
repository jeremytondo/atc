import Foundation
import Testing
@testable import ATC

@Suite("RefreshState")
struct RefreshStateTests {
    @Test("a failed refresh keeps data and records the message")
    func failureRecorded() async {
        let state = RefreshState(category: "test")
        struct Boom: Error, LocalizedError { var errorDescription: String? { "boom" } }
        await state.run { throw Boom() } apply: { (_: Int) in }
        #expect(state.lastError == "boom")
        #expect(state.hasLoadedOnce)
        #expect(!state.isResolved)
    }

    @Test("a cancelled refresh is not a failure and never resolves the store")
    func cancellationIgnored() async {
        let state = RefreshState(category: "test")
        await state.run { throw CancellationError() } apply: { (_: Int) in }
        #expect(state.lastError == nil)
        #expect(!state.hasLoadedOnce)
    }

    @Test("an error surfaced by task cancellation is not recorded either")
    func cancelledTaskErrorIgnored() async {
        let state = RefreshState(category: "test")
        struct TransportTornDown: Error {}
        let task = Task {
            await state.run {
                // Simulates a transport that reports cancellation as its own
                // error type rather than CancellationError.
                try? await Task.sleep(for: .seconds(10))
                if Task.isCancelled { throw TransportTornDown() }
                return 0
            } apply: { (_: Int) in }
        }
        task.cancel()
        await task.value
        #expect(state.lastError == nil)
        #expect(!state.hasLoadedOnce)
    }

    @Test("a successful refresh after a cancelled one resolves cleanly")
    func recoveryAfterCancellation() async {
        let state = RefreshState(category: "test")
        await state.run { throw CancellationError() } apply: { (_: Int) in }
        var applied: Int?
        await state.run { 7 } apply: { applied = $0 }
        #expect(applied == 7)
        #expect(state.isResolved)
    }
}

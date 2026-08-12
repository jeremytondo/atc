import Foundation
import Testing
import ATCAppServerTransport
import GhosttyTerminal
@testable import ATC

/// Counts `onTerminalEnded` firings. A reference box keeps the callback's
/// capture explicit instead of relying on a local variable's storage.
private final class EndedRecorder {
    var count = 0
}

/// The attach end-state discipline: a socket close says nothing about the
/// terminal, so every retryable end goes through a reconciled server read
/// before a reconnect is scheduled — and only an authoritative end (or a read
/// that proves the terminal gone) is allowed to look like a dead terminal.
@MainActor
@Suite("Terminal reconnect")
struct TerminalReconnectTests {
    private func makeController(
        harness: AttachHarness,
        delays: RetryDelayRecorder,
        live: CheckLiveStub,
        policy: TerminalSessionController.RetryPolicy? = nil,
        now: (() -> ContinuousClock.Instant)? = nil
    ) -> TerminalSessionController {
        TerminalSessionController(
            terminalID: "trm_1",
            endpoint: AttachEndpoint(baseURL: URL(string: "http://a:1")!, headers: [:]),
            checkLive: live.check,
            connectionFactory: harness.makeHandle,
            retryPolicy: policy ?? .default,
            retrySleep: delays.sleep,
            jitterUnit: { 0.5 },
            now: now ?? { ContinuousClock.now }
        )
    }

    private func backendIdentity(of controller: TerminalSessionController) -> ObjectIdentifier {
        guard case .inMemory(let session) = controller.viewState.configuration.backend else {
            fatalError("terminal controller must use the in-memory backend")
        }
        return ObjectIdentifier(session)
    }

    @Test("retry policy doubles, jitters symmetrically, and never exceeds its cap")
    func retryPolicy() {
        let policy = TerminalSessionController.RetryPolicy.default

        #expect(policy.delay(forAttempt: 0, jitterUnit: 0) == .milliseconds(400))
        #expect(policy.delay(forAttempt: 0, jitterUnit: 0.5) == .milliseconds(500))
        #expect(policy.delay(forAttempt: 0, jitterUnit: 1) == .milliseconds(600))
        #expect((0...5).map { policy.delay(forAttempt: $0, jitterUnit: 0.5) } == [
            .milliseconds(500),
            .seconds(1),
            .seconds(2),
            .seconds(4),
            .seconds(8),
            .seconds(8),
        ])
        #expect(policy.delay(forAttempt: 20, jitterUnit: 1) == .seconds(8))
    }

    @Test("a retryable close whose server read says gone ends authoritatively")
    func retryableWithDeadTerminalEnds() async {
        let harness = AttachHarness()
        let delays = RetryDelayRecorder()
        let live = CheckLiveStub(result: false)
        let controller = makeController(harness: harness, delays: delays, live: live)
        defer { controller.disconnect() }
        let ended = EndedRecorder()
        controller.onTerminalEnded = { ended.count += 1 }

        harness.send(.ended(.retryable("socket closed")), attempt: 0, finish: true)
        await settle(until: { controller.phase == .ended(.terminalEnded) })

        #expect(controller.phase == .ended(.terminalEnded))
        #expect(ended.count == 1)
        #expect(!controller.isActivelyAttached)
        // The server answered: no backoff was scheduled and no socket reopened.
        #expect(delays.delays.isEmpty)
        #expect(harness.attemptCount == 1)
        #expect(live.callCount == 1)
    }

    @Test("a dead-verified terminal never auto-recovers")
    func deadVerifiedTerminalStaysEnded() async {
        let harness = AttachHarness()
        let delays = RetryDelayRecorder()
        let live = CheckLiveStub(result: false)
        let controller = makeController(harness: harness, delays: delays, live: live)
        defer { controller.disconnect() }

        harness.send(.ended(.retryable("socket closed")), attempt: 0, finish: true)
        await settle(until: { controller.phase == .ended(.terminalEnded) })

        // The drift this pins: a verification-driven end must disarm
        // recovery like every other end path — a wake signal must never
        // reopen a socket to a terminal the server already said is gone.
        controller.recoverAfterInterruption()
        await drainPendingTasks()
        #expect(harness.attemptCount == 1)
        #expect(controller.phase == .ended(.terminalEnded))
        #expect(delays.delays.isEmpty)
    }

    @Test("a retryable close whose server read says live reconnects on backoff")
    func retryableWithLiveTerminalReconnects() async {
        let harness = AttachHarness()
        let delays = RetryDelayRecorder()
        let live = CheckLiveStub(result: true)
        let controller = makeController(harness: harness, delays: delays, live: live)
        defer { controller.disconnect() }
        let ended = EndedRecorder()
        controller.onTerminalEnded = { ended.count += 1 }

        harness.send(.ended(.retryable("socket closed")), attempt: 0, finish: true)
        await settle(until: { harness.attemptCount == 2 })

        #expect(delays.delays == [.milliseconds(500)])
        // The banner reads "Reconnecting" throughout; no `.ended` flicker.
        #expect(controller.phase == .reconnecting)
        #expect(controller.isActivelyAttached)
        #expect(ended.count == 0)

        // A connect that greets and immediately drops must NOT reset the
        // budget — backoff keeps escalating (the stability rule; a stable
        // connection's reset is covered separately below).
        harness.send(.connected, attempt: 1)
        await settle(until: { controller.phase == .connected })
        harness.send(.ended(.retryable("again")), attempt: 1, finish: true)
        await settle(until: { harness.attemptCount == 3 })
        #expect(delays.delays == [.milliseconds(500), .milliseconds(1000)])
    }

    @Test("only a stable connection resets the retry budget")
    func stableConnectionResetsBudget() async {
        let harness = AttachHarness()
        let delays = RetryDelayRecorder()
        let live = CheckLiveStub(result: true)
        // A fake clock the test advances: stability is measured, not slept.
        let clock = FakeClock()
        let controller = makeController(
            harness: harness, delays: delays, live: live, now: { clock.now }
        )
        defer { controller.disconnect() }

        harness.send(.ended(.retryable("drop")), attempt: 0, finish: true)
        await settle(until: { harness.attemptCount == 2 })
        #expect(delays.delays == [.milliseconds(500)])

        harness.send(.connected, attempt: 1)
        await settle(until: { controller.phase == .connected })
        // The connection lives past the stability threshold before dropping.
        clock.advance(by: .seconds(10))
        harness.send(.ended(.retryable("later")), attempt: 1, finish: true)
        await settle(until: { harness.attemptCount == 3 })
        #expect(delays.delays == [.milliseconds(500), .milliseconds(500)])
    }

    @Test("a server that cannot be asked still reconnects rather than guessing")
    func retryableWithUnknownLivenessReconnects() async {
        let harness = AttachHarness()
        let delays = RetryDelayRecorder()
        let live = CheckLiveStub(result: nil)
        let controller = makeController(harness: harness, delays: delays, live: live)
        defer { controller.disconnect() }

        harness.send(.ended(.retryable("offline")), attempt: 0, finish: true)
        await settle(until: { harness.attemptCount == 2 })
        #expect(delays.delays == [.milliseconds(500)])
        #expect(controller.phase == .reconnecting)

        // Backoff keeps escalating while liveness stays unknowable.
        harness.send(.ended(.retryable("offline")), attempt: 1, finish: true)
        await settle(until: { harness.attemptCount == 3 })
        #expect(delays.delays == [.milliseconds(500), .seconds(1)])
    }

    @Test("authoritative ends stop immediately and never recover")
    func authoritativeEndsStayStopped() async {
        for reason in [AttachEndReason.terminalEnded, .rejected(statusCode: 404)] {
            let harness = AttachHarness()
            let delays = RetryDelayRecorder()
            let live = CheckLiveStub(result: true)
            let controller = makeController(harness: harness, delays: delays, live: live)
            let ended = EndedRecorder()
            controller.onTerminalEnded = { ended.count += 1 }

            harness.send(.ended(reason), attempt: 0, finish: true)
            await settle(until: { controller.phase == .ended(reason) })

            #expect(controller.phase == .ended(reason))
            #expect(ended.count == 1)
            // Not even a liveness read: the close was authoritative.
            #expect(live.callCount == 0)

            controller.recoverAfterInterruption()
            await drainPendingTasks()
            #expect(harness.attemptCount == 1)
            #expect(delays.delays.isEmpty)
            #expect(controller.phase == .ended(reason))
            controller.disconnect()
        }
    }

    @Test("automatic retries stop at the policy budget; manual reconnect restores it")
    func retryBudgetExhaustion() async {
        let harness = AttachHarness()
        let delays = RetryDelayRecorder()
        let live = CheckLiveStub(result: true)
        let policy = TerminalSessionController.RetryPolicy(
            baseDelayMilliseconds: 500,
            maximumDelayMilliseconds: 8_000,
            jitterFraction: 0.2,
            maximumAttempts: 2
        )
        let controller = makeController(
            harness: harness, delays: delays, live: live, policy: policy
        )
        defer { controller.disconnect() }

        harness.send(.ended(.retryable("offline")), attempt: 0, finish: true)
        await settle(until: { harness.attemptCount == 2 })
        harness.send(.ended(.retryable("offline")), attempt: 1, finish: true)
        await settle(until: { harness.attemptCount == 3 })
        #expect(delays.delays.count == 2)

        // The budget is spent: a permanent failure must stop churning.
        let exhausted = AttachEndReason.retryable("offline")
        harness.send(.ended(exhausted), attempt: 2, finish: true)
        await settle(until: { controller.phase == .ended(exhausted) })
        #expect(harness.attemptCount == 3)
        #expect(delays.delays.count == 2)
        #expect(!controller.isActivelyAttached)

        // Manual reconnect resets the budget and skips the pending wait.
        controller.reconnect()
        #expect(harness.attemptCount == 4)
        #expect(controller.phase == .reconnecting)
        harness.send(.ended(.retryable("offline")), attempt: 3, finish: true)
        await settle(until: { harness.attemptCount == 5 })
        #expect(delays.delays.count == 3)
    }

    @Test("wake and path recovery revive a given-up controller with a fresh budget")
    func recoveryRestoresBudget() async {
        let harness = AttachHarness()
        let delays = RetryDelayRecorder()
        let live = CheckLiveStub(result: true)
        let policy = TerminalSessionController.RetryPolicy(
            baseDelayMilliseconds: 500,
            maximumDelayMilliseconds: 8_000,
            jitterFraction: 0.2,
            maximumAttempts: 1
        )
        let controller = makeController(
            harness: harness, delays: delays, live: live, policy: policy
        )
        defer { controller.disconnect() }

        harness.send(.ended(.retryable("offline")), attempt: 0, finish: true)
        await settle(until: { harness.attemptCount == 2 })
        let exhausted = AttachEndReason.retryable("offline")
        harness.send(.ended(exhausted), attempt: 1, finish: true)
        await settle(until: { controller.phase == .ended(exhausted) })

        // A healed environment must not require a manual click.
        controller.recoverAfterInterruption()
        #expect(harness.attemptCount == 3)
        #expect(controller.phase == .reconnecting)
    }

    @Test("disconnect is a detach: ended by client, and never auto-recovered")
    func disconnectStopsRecovery() async {
        let harness = AttachHarness()
        let delays = RetryDelayRecorder()
        let live = CheckLiveStub(result: true)
        let controller = makeController(harness: harness, delays: delays, live: live)

        harness.send(.connected, attempt: 0)
        await settle(until: { controller.phase == .connected })

        controller.disconnect()
        #expect(controller.phase == .ended(.closedByClient))
        #expect(!controller.isActivelyAttached)

        controller.recoverAfterInterruption()
        await drainPendingTasks()
        #expect(harness.attemptCount == 1)
        #expect(controller.phase == .ended(.closedByClient))
        #expect(delays.delays.isEmpty)
    }

    @Test("a close reported by a replaced attach never overwrites the new one")
    func staleEventsAreGenerationGated() async {
        let harness = AttachHarness()
        let delays = RetryDelayRecorder()
        let live = CheckLiveStub(result: true)
        let controller = makeController(harness: harness, delays: delays, live: live)
        defer { controller.disconnect() }

        let initialBackend = backendIdentity(of: controller)
        harness.send(.connected, attempt: 0)
        await settle(until: { controller.phase == .connected })

        controller.recoverAfterInterruption()
        #expect(harness.attemptCount == 2)
        // Preserve the visible screen while a reconnect is merely trying to
        // establish transport; no replay exists to justify clearing it yet.
        #expect(backendIdentity(of: controller) == initialBackend)

        harness.send(.ended(.terminalEnded), attempt: 0, finish: true)
        await drainPendingTasks()
        #expect(controller.phase == .reconnecting)

        // zmx replays the whole screen on reattach, so the backend is swapped
        // only once transport actually opens.
        harness.send(.connected, attempt: 1)
        await settle(until: { controller.phase == .connected })
        #expect(backendIdentity(of: controller) != initialBackend)
    }

    @Test("the app's recovery monitor reconnects live attaches and leaves ended ones alone")
    func recoveryMonitorIntegration() async throws {
        let center = NotificationCenter()
        let wake = Notification.Name("TerminalReconnectTests.wake")
        let monitor = TerminalRecoveryMonitor(
            notificationCenter: center,
            wakeNotification: wake,
            pathMonitor: nil
        )
        let harness = AttachHarness()
        let client = ScriptableAppServerClient()
        Fixtures.seed(client)
        let store = makeConnectionsStore()
        let record = try store.add(name: "A", urlString: "http://a:1", token: "")
        let model = AppModel(
            connections: store,
            clientFactory: { _, _ in client },
            terminalControllerFactory: { terminalID, runtime in
                makeTestController(terminalID: terminalID, runtime: runtime, attach: harness)
            },
            terminalRecoveryMonitor: monitor,
            eventStreamFactory: { _, _ in ScriptedEventStream.inert() }
        )
        let runtime = try #require(model.runtime(id: record.id))
        // Settle the runtime's first-contact probe instead of refreshing
        // manually: only the newest in-flight refresh applies, so a manual
        // refresh racing the probe can be discarded and return early.
        await settle(until: { runtime.terminals.isResolved })
        let terminal = try #require(runtime.terminals.terminal(id: "trm_live"))
        model.attachIfNeeded(to: terminal, connectionID: record.id)
        let ref = TerminalRef(connectionID: record.id, terminalID: terminal.id)
        let controller = try #require(model.terminals[ref])

        harness.send(.connected, attempt: 0)
        await settle(until: { controller.phase == .connected })

        // NWPathMonitor always reports its current path after start; a healthy
        // initial value must not duplicate the initial attach.
        monitor.recordNetworkPath(isSatisfied: true)
        #expect(harness.attemptCount == 1)
        monitor.recordNetworkPath(isSatisfied: false)
        monitor.recordNetworkPath(isSatisfied: true)
        #expect(harness.attemptCount == 2)

        harness.send(.connected, attempt: 1)
        await settle(until: { controller.phase == .connected })
        center.post(name: wake, object: nil)
        await settle(until: { harness.attemptCount == 3 })
        #expect(harness.attemptCount == 3)

        // Once the terminal is proven gone, no recovery source may revive it.
        harness.send(.ended(.terminalEnded), attempt: 2, finish: true)
        await settle(until: { controller.phase == .ended(.terminalEnded) })
        monitor.recordNetworkPath(isSatisfied: false)
        monitor.recordNetworkPath(isSatisfied: true)
        center.post(name: wake, object: nil)
        await drainPendingTasks()
        #expect(harness.attemptCount == 3)
    }
}

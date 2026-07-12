import Foundation
import Testing
import ATCAPI
import GhosttyTerminal
@testable import ATC

private final class AttachHarness {
    struct Attempt {
        let continuation: AsyncStream<AttachEvent>.Continuation
    }

    private(set) var attempts: [Attempt] = []

    func makeHandle(url _: URL, headers _: [String: String]) -> TerminalAttachHandle {
        let (stream, continuation) = AsyncStream.makeStream(of: AttachEvent.self)
        attempts.append(Attempt(continuation: continuation))
        return TerminalAttachHandle(
            start: { stream },
            enqueue: { _ in },
            enqueueResize: { _, _ in },
            // Intentionally leave the stream open. This lets tests prove a
            // late event from a replaced attach is generation-gated.
            close: {}
        )
    }

    func send(_ event: AttachEvent, attempt: Int, finish: Bool = false) {
        attempts[attempt].continuation.yield(event)
        if finish {
            attempts[attempt].continuation.finish()
        }
    }
}

private final class RetryDelayRecorder {
    private(set) var delays: [Duration] = []

    func sleep(for duration: Duration) async throws {
        delays.append(duration)
    }
}

@MainActor
private func waitFor(_ condition: () -> Bool) async {
    for _ in 0..<200 {
        if condition() { return }
        await Task.yield()
    }
}

@MainActor
private func backendIdentity(of controller: TerminalSessionController) -> ObjectIdentifier {
    guard case .inMemory(let session) = controller.viewState.configuration.backend else {
        fatalError("terminal controller must use the in-memory backend")
    }
    return ObjectIdentifier(session)
}

@MainActor
@Suite("Terminal reconnect")
struct TerminalReconnectTests {
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

    @Test("transport and server failures retry with deterministic exponential delays")
    func retryableFailures() async {
        let harness = AttachHarness()
        let delays = RetryDelayRecorder()
        let controller = TerminalSessionController(
            sessionID: "session",
            client: MockATCClient(),
            connectionFactory: harness.makeHandle,
            retrySleep: delays.sleep,
            jitterUnit: { 0.5 }
        )
        defer { controller.disconnect() }

        #expect(harness.attempts.count == 1)
        harness.send(.ended(.transportFailure("offline")), attempt: 0, finish: true)
        await waitFor { harness.attempts.count == 2 }
        #expect(delays.delays == [.milliseconds(500)])
        #expect(harness.attempts.count == 2)

        harness.send(.ended(.serverError), attempt: 1, finish: true)
        await waitFor { harness.attempts.count == 3 }
        #expect(delays.delays == [.milliseconds(500), .seconds(1)])

        // A successful attach resets the failure sequence.
        harness.send(.connected, attempt: 2)
        await waitFor { controller.phase == .connected }
        harness.send(.ended(.transportFailure("again")), attempt: 2, finish: true)
        await waitFor { harness.attempts.count == 4 }
        #expect(delays.delays == [.milliseconds(500), .seconds(1), .milliseconds(500)])
    }

    @Test("automatic retries stop at the policy budget; recovery signals restore it")
    func retryBudgetExhaustion() async {
        let harness = AttachHarness()
        let delays = RetryDelayRecorder()
        let policy = TerminalSessionController.RetryPolicy(
            baseDelayMilliseconds: 500,
            maximumDelayMilliseconds: 8_000,
            jitterFraction: 0.2,
            maximumAttempts: 2
        )
        let controller = TerminalSessionController(
            sessionID: "session",
            client: MockATCClient(),
            connectionFactory: harness.makeHandle,
            retryPolicy: policy,
            retrySleep: delays.sleep,
            jitterUnit: { 0.5 }
        )
        defer { controller.disconnect() }

        harness.send(.ended(.transportFailure("offline")), attempt: 0, finish: true)
        await waitFor { harness.attempts.count == 2 }
        harness.send(.ended(.transportFailure("offline")), attempt: 1, finish: true)
        await waitFor { harness.attempts.count == 3 }
        #expect(delays.delays.count == 2)

        // The budget is spent: the third failure must give up rather than
        // churn forever against a permanent failure.
        harness.send(.ended(.transportFailure("offline")), attempt: 2, finish: true)
        await waitFor { controller.phase == .ended(.transportFailure("offline")) }
        await Task.yield()
        #expect(harness.attempts.count == 3)
        #expect(delays.delays.count == 2)
        #expect(!controller.isActivelyAttached)

        // Wake/path recovery revives a given-up controller with a fresh
        // budget — a healed environment must not require a manual click.
        controller.recoverAfterInterruption()
        #expect(harness.attempts.count == 4)
        #expect(controller.phase == .reconnecting)
        harness.send(.ended(.transportFailure("offline")), attempt: 3, finish: true)
        await waitFor { harness.attempts.count == 5 }
        #expect(delays.delays.count == 3)
    }

    @Test("a pending backoff reports reconnecting and manual reconnect skips the wait")
    func pendingRetryReportsReconnecting() async {
        let harness = AttachHarness()
        // A one-hour backoff (with the default real-clock sleep) pins the
        // controller in the waiting-for-retry window for the whole test.
        let policy = TerminalSessionController.RetryPolicy(
            baseDelayMilliseconds: 3_600_000,
            maximumDelayMilliseconds: 3_600_000,
            jitterFraction: 0,
            maximumAttempts: 10
        )
        let controller = TerminalSessionController(
            sessionID: "session",
            client: MockATCClient(),
            connectionFactory: harness.makeHandle,
            retryPolicy: policy,
            jitterUnit: { 0.5 }
        )
        defer { controller.disconnect() }

        harness.send(.ended(.transportFailure("offline")), attempt: 0, finish: true)
        await waitFor { controller.phase == .reconnecting }
        // No `.ended` flicker while the timer waits, and indicators still
        // treat the terminal as attached.
        #expect(controller.phase == .reconnecting)
        #expect(controller.isActivelyAttached)
        #expect(harness.attempts.count == 1)

        controller.reconnect()
        await waitFor { harness.attempts.count == 2 }
        #expect(harness.attempts.count == 2)
        #expect(controller.phase == .reconnecting)
    }

    @Test("terminal end and client close never auto-retry or recover")
    func terminalEndStatesStayStopped() async {
        for reason in [AttachEndReason.sessionEnded, .closedByClient] {
            let harness = AttachHarness()
            let delays = RetryDelayRecorder()
            let controller = TerminalSessionController(
                sessionID: "session",
                client: MockATCClient(),
                connectionFactory: harness.makeHandle,
                retrySleep: delays.sleep,
                jitterUnit: { 0.5 }
            )

            harness.send(.ended(reason), attempt: 0, finish: true)
            await waitFor { controller.phase == .ended(reason) }
            controller.recoverAfterInterruption()
            await Task.yield()
            #expect(harness.attempts.count == 1)
            #expect(delays.delays.isEmpty)
            #expect(controller.phase == .ended(reason))
            controller.disconnect()
        }
    }

    @Test("recovery replaces a live attach and its Ghostty backend, then ignores stale events")
    func recoveryReplacesSurfaceAndGatesOldEvents() async {
        let harness = AttachHarness()
        let controller = TerminalSessionController(
            sessionID: "session",
            client: MockATCClient(),
            connectionFactory: harness.makeHandle,
            retrySleep: { _ in },
            jitterUnit: { 0.5 }
        )
        defer { controller.disconnect() }

        let initialBackend = backendIdentity(of: controller)
        harness.send(.connected, attempt: 0)
        await waitFor { controller.phase == .connected }

        controller.recoverAfterInterruption()
        #expect(harness.attempts.count == 2)
        // Preserve the visible screen while a reconnect is merely trying to
        // establish transport; no replay exists to justify clearing it yet.
        #expect(backendIdentity(of: controller) == initialBackend)

        // The old connection may report its close after the replacement has
        // started. It must not overwrite the new attempt's phase or policy.
        harness.send(.ended(.sessionEnded), attempt: 0, finish: true)
        await Task.yield()
        #expect(controller.phase == .reconnecting)

        // A reconnect that fails before opening also preserves the surface.
        harness.send(.ended(.transportFailure("still offline")), attempt: 1, finish: true)
        await waitFor { harness.attempts.count == 3 }
        #expect(backendIdentity(of: controller) == initialBackend)

        // The backend is replaced only once transport opens, immediately
        // before the controller sends the resize that triggers zmx replay.
        harness.send(.connected, attempt: 2)
        await waitFor { controller.phase == .connected }
        #expect(backendIdentity(of: controller) != initialBackend)
        #expect(controller.phase == .connected)
    }

    @Test("initial network satisfaction is ignored; recovery and wake reconnect app terminals")
    func monitorAndAppModelIntegration() async throws {
        let center = NotificationCenter()
        let wake = Notification.Name("TerminalReconnectTests.wake")
        let monitor = TerminalRecoveryMonitor(
            notificationCenter: center,
            wakeNotification: wake,
            pathMonitor: nil
        )
        let harness = AttachHarness()
        let suite = "TerminalReconnectTests.\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        defaults.removePersistentDomain(forName: suite)
        let store = ConnectionsStore(defaults: defaults, credentials: InMemoryCredentialStore())
        let record = try store.add(name: "Test", urlString: "http://test:7331", token: "")
        let model = AppModel(
            connections: store,
            clientFactory: { _ in MockATCClient() },
            terminalControllerFactory: { sessionID, client in
                TerminalSessionController(
                    sessionID: sessionID,
                    client: client,
                    connectionFactory: harness.makeHandle,
                    retrySleep: { _ in },
                    jitterUnit: { 0.5 }
                )
            },
            terminalRecoveryMonitor: monitor
        )
        let runtime = try #require(model.runtime(id: record.id))
        runtime.stopPolling()
        await runtime.refresh()
        let session = try #require(runtime.sessions.sessions.first { $0.attachable })
        model.attachIfNeeded(to: session, connectionID: record.id)
        let ref = SessionRef(connectionID: record.id, sessionID: session.id)
        let controller = try #require(model.terminals[ref])
        harness.send(.connected, attempt: 0)
        await waitFor { controller.phase == .connected }

        // NWPathMonitor always reports its current path after start. A
        // healthy initial value must not duplicate the initial attach.
        monitor.recordNetworkPath(isSatisfied: true)
        #expect(harness.attempts.count == 1)
        monitor.recordNetworkPath(isSatisfied: false)
        monitor.recordNetworkPath(isSatisfied: true)
        #expect(harness.attempts.count == 2)

        harness.send(.connected, attempt: 1)
        await waitFor { controller.phase == .connected }
        center.post(name: wake, object: nil)
        await waitFor { harness.attempts.count == 3 }
        #expect(harness.attempts.count == 3)

        // Once the server reports the session ended, neither recovery source
        // is allowed to revive it.
        harness.send(.ended(.sessionEnded), attempt: 2, finish: true)
        await waitFor { controller.phase == .ended(.sessionEnded) }
        monitor.recordNetworkPath(isSatisfied: false)
        monitor.recordNetworkPath(isSatisfied: true)
        center.post(name: wake, object: nil)
        await Task.yield()
        #expect(harness.attempts.count == 3)
    }
}

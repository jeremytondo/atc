import Foundation
import Observation
import OSLog
import ATCAPI
import GhosttyTerminal

private let logger = Logger(subsystem: "ElevenIdeas.atc", category: "terminal")

/// Bridges one atc session's attach WebSocket to one Ghostty surface.
/// Lives in the AppModel registry so the surface and connection survive
/// sidebar switches; views only render it.
@Observable
final class TerminalSessionController: Identifiable {
    struct RetryPolicy: Sendable {
        let baseDelayMilliseconds: Int64
        let maximumDelayMilliseconds: Int64
        let jitterFraction: Double
        /// Consecutive automatic retries before giving up. A permanent
        /// failure (revoked token, deleted server) surfaces as an endless
        /// stream of transport failures; without a budget the controller
        /// would churn connection attempts forever. Manual reconnect and
        /// wake/path recovery reset the budget, so an environment that
        /// heals later still recovers without user action.
        let maximumAttempts: Int

        static let `default` = RetryPolicy(
            baseDelayMilliseconds: 500,
            maximumDelayMilliseconds: 8_000,
            jitterFraction: 0.2,
            maximumAttempts: 10
        )

        /// Exponential delay with symmetric jitter. The final value is
        /// capped too, so a positive jitter never exceeds the advertised
        /// maximum.
        func delay(forAttempt attempt: Int, jitterUnit: Double) -> Duration {
            var nominal = baseDelayMilliseconds
            for _ in 0..<max(0, attempt) {
                nominal = min(maximumDelayMilliseconds, nominal * 2)
            }

            let unit = min(1, max(0, jitterUnit))
            let factor = (1 - jitterFraction) + (2 * jitterFraction * unit)
            let jittered = Int64((Double(nominal) * factor).rounded())
            return .milliseconds(min(maximumDelayMilliseconds, max(0, jittered)))
        }
    }

    enum Phase: Equatable {
        case connecting
        case connected
        /// A dropped attach is being replaced automatically — either a
        /// backoff timer is pending or a replacement attempt is in flight.
        /// Distinct from `.connecting` so the status banner reads
        /// "Reconnecting" instead of flickering through `.ended` and
        /// "Connecting" on every retry cycle.
        case reconnecting
        case ended(AttachEndReason)

        /// Whether the WebSocket bridge is (or is becoming) live. Ended
        /// controllers stay in the AppModel registry for scrollback, so
        /// connection indicators must check this, not registry membership.
        var isActivelyAttached: Bool {
            switch self {
            case .connecting, .connected, .reconnecting: return true
            case .ended: return false
            }
        }
    }

    /// One Ghostty runtime/config for all surfaces, created lazily with the
    /// launch-resolved preferences (ATCApp loads the store before any
    /// terminal exists) and updated in place on reload.
    @MainActor private static var sharedGhostty: TerminalController?
    @MainActor private static var currentPreferences = TerminalPreferences()

    @MainActor
    static func applyTerminalPreferences(_ preferences: TerminalPreferences) {
        currentPreferences = preferences
        if let sharedGhostty {
            TerminalPresentation.apply(preferences: preferences, to: sharedGhostty)
        }
    }

    @MainActor
    private static func sharedGhosttyController() -> TerminalController {
        if let sharedGhostty {
            return sharedGhostty
        }
        let controller = TerminalPresentation.makeController(preferences: currentPreferences)
        sharedGhostty = controller
        return controller
    }

    let sessionID: String
    let viewState: TerminalViewState
    private(set) var phase: Phase = .connecting
    @ObservationIgnored var onSessionEnded: (() -> Void)?

    var id: String { sessionID }

    /// See `Phase.isActivelyAttached` — liveness for indicators and
    /// destructive-action confirmation, distinct from retained history.
    var isActivelyAttached: Bool { phase.isActivelyAttached }

    private let attachURL: URL
    private let attachHeaders: [String: String]
    private var terminalSession: InMemoryTerminalSession
    private let connectionRef: ConnectionRef
    private let connectionFactory: TerminalAttachFactory
    private let retryPolicy: RetryPolicy
    private let retrySleep: (Duration) async throws -> Void
    private let jitterUnit: () -> Double
    private var connection: TerminalAttachHandle?
    private var eventTask: Task<Void, Never>?
    private var retryTask: Task<Void, Never>?
    private var connectionGeneration = 0
    private var retryGeneration = 0
    private var retryAttempt = 0
    private var automaticRecoveryEnabled = true
    private var replayResetGeneration: Int?

    /// Output received before the Ghostty surface exists (zmx replays the
    /// screen immediately on attach) is buffered, then drained in order.
    private var pendingOutput: [Data] = []
    private var drainTask: Task<Void, Never>?
    private var surfaceIsReady = false

    init(
        sessionID: String,
        client: any ATCClient,
        connectionFactory: @escaping TerminalAttachFactory = TerminalAttachHandle.live,
        retryPolicy: RetryPolicy = .default,
        retrySleep: @escaping (Duration) async throws -> Void = { duration in
            try await Task.sleep(for: duration)
        },
        jitterUnit: @escaping () -> Double = { Double.random(in: 0...1) }
    ) {
        self.sessionID = sessionID
        attachURL = client.attachURL(sessionID: sessionID)
        attachHeaders = client.attachHeaders()
        self.connectionFactory = connectionFactory
        self.retryPolicy = retryPolicy
        self.retrySleep = retrySleep
        self.jitterUnit = jitterUnit

        let ref = ConnectionRef()
        connectionRef = ref
        // Both callbacks fire on Ghostty threads. Going through the
        // lock-guarded ref and the connection's synchronous enqueue keeps
        // byte order intact — no Task-hop reordering.
        terminalSession = Self.makeTerminalSession(connectionRef: ref)

        viewState = TerminalViewState(controller: Self.sharedGhosttyController())
        viewState.configuration = TerminalSurfaceOptions(backend: .inMemory(terminalSession))

        connect(isReconnect: false)
    }

    // MARK: - Connection lifecycle

    func reconnect() {
        guard connection == nil || phase != .connected else { return }
        automaticRecoveryEnabled = true
        retryAttempt = 0
        beginReconnect()
    }

    /// A wake or recovered network path can leave URLSession believing a
    /// dead WebSocket is still open. Replace any controller that should
    /// still be attached, including one whose stale socket has not failed
    /// locally yet. Terminal/session end states deliberately opt out.
    func recoverAfterInterruption() {
        guard automaticRecoveryEnabled else { return }
        retryAttempt = 0
        beginReconnect()
    }

    /// Detach: WS close = zmx detach; the session keeps running server-side.
    func disconnect() {
        automaticRecoveryEnabled = false
        cancelRetry()
        let connection = connection
        self.connection = nil
        connectionRef.set(nil)
        eventTask?.cancel()
        eventTask = nil
        drainTask?.cancel()
        drainTask = nil
        phase = .ended(.closedByClient)
        Task { await connection?.close() }
    }

    private func beginReconnect() {
        cancelRetry()
        let connection = connection
        self.connection = nil
        connectionRef.set(nil)
        eventTask?.cancel()
        eventTask = nil
        Task { await connection?.close() }
        connect(isReconnect: true)
    }

    private func connect(isReconnect: Bool) {
        phase = isReconnect ? .reconnecting : .connecting
        connectionGeneration += 1
        let generation = connectionGeneration
        let connection = connectionFactory(attachURL, attachHeaders)
        self.connection = connection
        connectionRef.set(connection)

        eventTask = Task { [weak self] in
            let events = await connection.start()
            for await event in events {
                guard let self, !Task.isCancelled else { return }
                self.handle(event, generation: generation, isReconnect: isReconnect)
            }
        }
    }

    private func handle(_ event: AttachEvent, generation: Int, isReconnect: Bool) {
        guard generation == connectionGeneration else { return }
        switch event {
        case .connected:
            prepareSurfaceForReplayIfNeeded(generation: generation, isReconnect: isReconnect)
            retryAttempt = 0
            phase = .connected
            logger.debug("attached \(self.sessionID)")
            // zmx needs dimensions before it repaints; resend the last
            // known viewport as the mandatory initial resize.
            if let viewport = connectionRef.lastViewport {
                connection?.enqueueResize(cols: viewport.cols, rows: viewport.rows)
            }
        case .output(let data):
            // `connected` is expected first, but keep the replay safe if
            // URLSession ever delivers buffered bytes before its delegate
            // callback reaches this task.
            prepareSurfaceForReplayIfNeeded(generation: generation, isReconnect: isReconnect)
            deliver(data)
        case .ended(let reason):
            logger.debug("attach ended \(self.sessionID): \(String(describing: reason))")
            connection = nil
            connectionRef.set(nil)
            eventTask = nil
            switch reason {
            case .serverError, .transportFailure:
                // Stay in `.reconnecting` across backoff cycles so the banner
                // doesn't flicker. `.ended` is the give-up state: its banner
                // offers a manual Reconnect, and wake/path recovery still
                // revives the controller with a fresh retry budget.
                phase = scheduleAutomaticReconnect() ? .reconnecting : .ended(reason)
            case .sessionEnded, .closedByClient:
                automaticRecoveryEnabled = false
                retryAttempt = 0
                cancelRetry()
                phase = .ended(reason)
                if reason == .sessionEnded {
                    onSessionEnded?()
                }
            }
        }
    }

    /// Returns whether a retry was scheduled; false once the policy's
    /// attempt budget is spent (or recovery is disabled entirely).
    private func scheduleAutomaticReconnect() -> Bool {
        guard automaticRecoveryEnabled, retryTask == nil,
              retryAttempt < retryPolicy.maximumAttempts else { return false }
        let delay = retryPolicy.delay(forAttempt: retryAttempt, jitterUnit: jitterUnit())
        retryAttempt += 1
        retryGeneration += 1
        let generation = retryGeneration
        let sleep = retrySleep

        retryTask = Task { [weak self] in
            do {
                try await sleep(delay)
            } catch {
                return
            }
            guard !Task.isCancelled, let self,
                  generation == self.retryGeneration,
                  self.automaticRecoveryEnabled,
                  self.connection == nil
            else { return }
            self.retryTask = nil
            self.connect(isReconnect: true)
        }
        return true
    }

    private func cancelRetry() {
        retryGeneration += 1
        retryTask?.cancel()
        retryTask = nil
    }

    /// zmx sends a complete VT snapshot on every reattach. Feeding it into
    /// the retained Ghostty surface duplicates the already-rendered screen
    /// and scrollback, so reconnects swap to a fresh in-memory backend first.
    /// GhosttyTerminal treats a different backend identity as a surface
    /// configuration change and rebuilds the surface. The initial attach is
    /// intentionally left alone.
    private func prepareSurfaceForReplayIfNeeded(generation: Int, isReconnect: Bool) {
        guard isReconnect, replayResetGeneration != generation else { return }
        replayResetGeneration = generation
        drainTask?.cancel()
        drainTask = nil
        pendingOutput.removeAll(keepingCapacity: true)
        surfaceIsReady = false
        terminalSession = Self.makeTerminalSession(connectionRef: connectionRef)
        viewState.configuration = TerminalSurfaceOptions(backend: .inMemory(terminalSession))
    }

    private static func makeTerminalSession(connectionRef: ConnectionRef) -> InMemoryTerminalSession {
        InMemoryTerminalSession(
            write: { data in
                connectionRef.enqueue(data)
            },
            resize: { viewport in
                connectionRef.enqueueResize(cols: viewport.columns, rows: viewport.rows)
            }
        )
    }

    // MARK: - Output delivery

    private func deliver(_ data: Data) {
        if surfaceIsReady && pendingOutput.isEmpty {
            terminalSession.receive(data)
            return
        }
        pendingOutput.append(data)
        startDrainIfNeeded()
    }

    private func startDrainIfNeeded() {
        guard drainTask == nil else { return }
        let terminalSession = terminalSession
        drainTask = Task {
            // Wait (bounded) for the surface to come up; readViewportText()
            // is nil until Ghostty attaches the surface.
            var attempts = 0
            while !surfaceIsReady, attempts < 100 {
                guard !Task.isCancelled else { return }
                if terminalSession.readViewportText() != nil { break }
                attempts += 1
                do {
                    try await Task.sleep(for: .milliseconds(50))
                } catch {
                    return
                }
            }
            // Proceed either way — receive() safely drops without a surface,
            // and marking ready stops unbounded buffering.
            guard !Task.isCancelled else { return }
            surfaceIsReady = true
            // Drain by index: repeated removeFirst() is quadratic on a large
            // replay, and the buffer must stay non-empty while draining so
            // concurrent deliver() calls keep appending behind the cursor.
            var index = 0
            while index < pendingOutput.count {
                terminalSession.receive(pendingOutput[index])
                index += 1
            }
            pendingOutput.removeAll()
            drainTask = nil
        }
    }
}

/// Sendable type erasure keeps the controller testable without widening
/// `AttachConnection` itself or making its actor API synchronous.
nonisolated struct TerminalAttachHandle: Sendable {
    private let startHandler: @Sendable () async -> AsyncStream<AttachEvent>
    private let enqueueHandler: @Sendable (Data) -> Void
    private let resizeHandler: @Sendable (UInt16, UInt16) -> Void
    private let closeHandler: @Sendable () async -> Void

    init(
        start: @escaping @Sendable () async -> AsyncStream<AttachEvent>,
        enqueue: @escaping @Sendable (Data) -> Void,
        enqueueResize: @escaping @Sendable (UInt16, UInt16) -> Void,
        close: @escaping @Sendable () async -> Void
    ) {
        startHandler = start
        enqueueHandler = enqueue
        resizeHandler = enqueueResize
        closeHandler = close
    }

    func start() async -> AsyncStream<AttachEvent> { await startHandler() }
    func enqueue(_ data: Data) { enqueueHandler(data) }
    func enqueueResize(cols: UInt16, rows: UInt16) { resizeHandler(cols, rows) }
    func close() async { await closeHandler() }

    static func live(url: URL, headers: [String: String]) -> TerminalAttachHandle {
        let connection = AttachConnection(url: url, headers: headers)
        return TerminalAttachHandle(
            start: { await connection.start() },
            enqueue: { connection.enqueue($0) },
            enqueueResize: { connection.enqueueResize(cols: $0, rows: $1) },
            close: { await connection.close() }
        )
    }
}

typealias TerminalAttachFactory = (URL, [String: String]) -> TerminalAttachHandle

/// Lock-guarded handle to the current connection so Ghostty's background
/// callbacks can enqueue synchronously across reconnects.
private nonisolated final class ConnectionRef: @unchecked Sendable {
    private let lock = NSLock()
    private var connection: TerminalAttachHandle?
    private var viewport: (cols: UInt16, rows: UInt16)?

    var lastViewport: (cols: UInt16, rows: UInt16)? {
        lock.withLock { viewport }
    }

    func set(_ connection: TerminalAttachHandle?) {
        lock.withLock { self.connection = connection }
    }

    func enqueue(_ data: Data) {
        lock.withLock { connection }?.enqueue(data)
    }

    func enqueueResize(cols: UInt16, rows: UInt16) {
        let connection = lock.withLock {
            viewport = (cols, rows)
            return self.connection
        }
        connection?.enqueueResize(cols: cols, rows: rows)
    }
}

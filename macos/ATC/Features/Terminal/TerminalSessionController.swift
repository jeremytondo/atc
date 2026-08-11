import Foundation
import Observation
import OSLog
import ATCAppServerTransport
import GhosttyTerminal

private let logger = Logger(subsystem: "ElevenIdeas.atc", category: "terminal")

/// Where a terminal's attach WebSocket lives: the Connection's origin plus
/// its transport headers (the bearer-auth seam).
struct AttachEndpoint: Sendable {
    let baseURL: URL
    let headers: [String: String]
}

/// Bridges one App Server terminal's attach WebSocket to one Ghostty
/// surface. Lives in the AppModel registry so the surface and connection
/// survive sidebar switches; views only render it.
///
/// End-state discipline per the attach protocol: only `terminalEnded` (or a
/// reconciled server read) proves the terminal is gone. Every retryable
/// close consults the server through `checkLive` before reconnecting, so a
/// socket drop is never presented as a dead terminal.
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
        /// A dropped attach is being replaced automatically — a server
        /// recheck, a backoff timer, or a replacement attempt is in flight.
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

    let terminalID: String
    let viewState: TerminalViewState
    private(set) var phase: Phase = .connecting
    /// Fired when the terminal may be gone — an authoritative close, a
    /// reconciled read, or a pre-upgrade rejection — so the owner refetches
    /// and the tombstone (or corrected state) lands.
    @ObservationIgnored var onTerminalEnded: (() -> Void)?

    var id: String { terminalID }

    /// See `Phase.isActivelyAttached` — liveness for indicators and
    /// destructive-action confirmation, distinct from retained history.
    var isActivelyAttached: Bool { phase.isActivelyAttached }

    private let endpoint: AttachEndpoint
    /// Reconciled server read: true/false is authoritative, nil means the
    /// server could not be asked (keep retrying rather than guessing).
    private let checkLive: () async -> Bool?
    private var terminalSession: InMemoryTerminalSession
    private let connectionRef: ConnectionRef
    private let connectionFactory: TerminalAttachFactory
    private let retryPolicy: RetryPolicy
    private let retrySleep: (Duration) async throws -> Void
    private let jitterUnit: () -> Double
    private let now: () -> ContinuousClock.Instant
    /// When the current attach reached `.connected`; nil while not open.
    private var connectionOpenedAt: ContinuousClock.Instant?
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
        terminalID: String,
        endpoint: AttachEndpoint,
        checkLive: @escaping () async -> Bool?,
        connectionFactory: @escaping TerminalAttachFactory = TerminalAttachHandle.live,
        retryPolicy: RetryPolicy = .default,
        retrySleep: @escaping (Duration) async throws -> Void = { duration in
            try await Task.sleep(for: duration)
        },
        jitterUnit: @escaping () -> Double = { Double.random(in: 0...1) },
        now: @escaping () -> ContinuousClock.Instant = { ContinuousClock.now }
    ) {
        self.terminalID = terminalID
        self.endpoint = endpoint
        self.checkLive = checkLive
        self.connectionFactory = connectionFactory
        self.retryPolicy = retryPolicy
        self.retrySleep = retrySleep
        self.jitterUnit = jitterUnit
        self.now = now

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
    /// locally yet. Terminal end states deliberately opt out.
    func recoverAfterInterruption() {
        guard automaticRecoveryEnabled else { return }
        retryAttempt = 0
        beginReconnect()
    }

    /// Detach: WS close = zmx detach; the terminal keeps running server-side.
    func disconnect() {
        end(.closedByClient, notify: false)
    }

    /// A reconciled read proved the terminal ended while this controller was
    /// not attached to hear the authoritative close: stop retrying and show
    /// the ended state, keeping the surface's final frame.
    func confirmEnded() {
        guard phase != .ended(.terminalEnded) else { return }
        end(.terminalEnded, notify: false)
    }

    /// The one authoritative end sequence: recovery off, retry state
    /// cleared, the connection released, the final phase published. Every
    /// path that ends the session — client detach, server end, rejected
    /// upgrade, dead-terminal verification — funnels here, so no branch can
    /// drift (a dead-verified terminal left recovery-armed was exactly such
    /// a drift).
    private func end(_ reason: AttachEndReason, notify: Bool) {
        automaticRecoveryEnabled = false
        retryAttempt = 0
        cancelRetry()
        releaseConnection()
        phase = .ended(reason)
        if notify { onTerminalEnded?() }
    }

    /// Shared connection teardown for the enders and the reconnect swap.
    /// The drain task deliberately survives it: buffered output keeps
    /// flowing to the retained surface (an ended terminal keeps its final
    /// frame; a reconnect's replay reset cancels the drain in
    /// prepareSurfaceForReplayIfNeeded when it swaps the surface).
    private func releaseConnection() {
        let connection = connection
        self.connection = nil
        connectionRef.set(nil)
        eventTask?.cancel()
        eventTask = nil
        Task { await connection?.close() }
    }

    private func beginReconnect() {
        cancelRetry()
        releaseConnection()
        connect(isReconnect: true)
    }

    private func connect(isReconnect: Bool) {
        phase = isReconnect ? .reconnecting : .connecting
        connectionGeneration += 1
        let generation = connectionGeneration
        // The attach URL carries the current viewport so the server sizes
        // the PTY before the repaint; reattaching also makes us the leader.
        let url = AttachProtocol.attachURL(
            base: endpoint.baseURL,
            terminalID: terminalID,
            size: connectionRef.lastViewport.map { (cols: Int($0.cols), rows: Int($0.rows)) }
        )
        let connection = connectionFactory(url, endpoint.headers)
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
            // The retry budget resets only once the connection proves stable
            // (see the `.retryable` end below): a peer that greets and
            // immediately drops must not defeat the budget and loop forever.
            connectionOpenedAt = now()
            phase = .connected
            logger.debug("attached \(self.terminalID)")
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
            logger.debug("attach ended \(self.terminalID): \(String(describing: reason))")
            connection = nil
            connectionRef.set(nil)
            eventTask = nil
            if let opened = connectionOpenedAt,
               now() - opened >= .milliseconds(retryPolicy.maximumDelayMilliseconds) {
                retryAttempt = 0
            }
            connectionOpenedAt = nil
            switch reason {
            case .retryable:
                // The socket says nothing about the terminal. Ask the
                // server before reconnecting; stay in `.reconnecting` so
                // the banner doesn't flicker through retry cycles.
                phase = .reconnecting
                verifyThenReconnect(afterEnd: reason)
            case .terminalEnded, .rejected:
                // Authoritative (or a pre-upgrade rejection that a refetch
                // must explain): stop and let stores reconcile.
                end(reason, notify: true)
            case .closedByClient:
                end(reason, notify: false)
            }
        }
    }

    /// The reconnect flow's first step: a reconciled read. A dead terminal
    /// becomes an authoritative end; a live (or unknowable) one proceeds to
    /// the backoff schedule.
    private func verifyThenReconnect(afterEnd reason: AttachEndReason) {
        let generation = connectionGeneration
        Task { [weak self] in
            guard let self else { return }
            let live = await self.checkLive()
            guard generation == self.connectionGeneration,
                  self.connection == nil,
                  self.automaticRecoveryEnabled
            else { return }
            if live == false {
                self.end(.terminalEnded, notify: true)
                return
            }
            if !self.scheduleAutomaticReconnect() {
                self.phase = .ended(reason)
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
        // [weak self] like every sibling task: a dropped controller must
        // deallocate, not live up to 5 seconds writing to an invisible
        // surface.
        drainTask = Task { [weak self] in
            // Wait (bounded) for the surface to come up; readViewportText()
            // is nil until Ghostty attaches the surface.
            var attempts = 0
            while self?.surfaceIsReady == false, attempts < 100 {
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
            guard let self, !Task.isCancelled else { return }
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
/// `AttachSocket` itself or making its actor API synchronous.
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
        let socket = AttachSocket(url: url, headers: headers)
        return TerminalAttachHandle(
            start: { await socket.start() },
            enqueue: { socket.enqueue($0) },
            enqueueResize: { socket.enqueueResize(cols: Int($0), rows: Int($1)) },
            close: { await socket.close() }
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

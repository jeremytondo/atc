import Foundation
import Observation
import OSLog
import CockpitAPI
import GhosttyTerminal

private let logger = Logger(subsystem: "ElevenIdeas.AtelierCode", category: "terminal")

/// Bridges one Cockpit session's attach WebSocket to one Ghostty surface.
/// Lives in the AppModel registry so the surface and connection survive
/// sidebar switches; views only render it.
@Observable
final class TerminalSessionController: Identifiable {
    enum Phase: Equatable {
        case connecting
        case connected
        case ended(AttachEndReason)
    }

    /// One Ghostty runtime/config for all surfaces.
    private static let sharedGhostty = GhosttyConfigLoader.makeController()

    let sessionID: String
    let viewState: TerminalViewState
    private(set) var phase: Phase = .connecting

    var id: String { sessionID }

    private let attachURL: URL
    private let attachHeaders: [String: String]
    private let terminalSession: InMemoryTerminalSession
    private let connectionRef: ConnectionRef
    private var connection: AttachConnection?
    private var eventTask: Task<Void, Never>?

    /// Output received before the Ghostty surface exists (zmx replays the
    /// screen immediately on attach) is buffered, then drained in order.
    private var pendingOutput: [Data] = []
    private var drainTask: Task<Void, Never>?
    private var surfaceIsReady = false

    init(sessionID: String, client: any CockpitClient) {
        self.sessionID = sessionID
        attachURL = client.attachURL(sessionID: sessionID)
        attachHeaders = client.attachHeaders()

        let ref = ConnectionRef()
        connectionRef = ref
        // Both callbacks fire on Ghostty threads. Going through the
        // lock-guarded ref and the connection's synchronous enqueue keeps
        // byte order intact — no Task-hop reordering.
        terminalSession = InMemoryTerminalSession(
            write: { data in
                ref.enqueue(data)
            },
            resize: { viewport in
                ref.enqueueResize(cols: viewport.columns, rows: viewport.rows)
            }
        )

        viewState = TerminalViewState(controller: Self.sharedGhostty)
        viewState.configuration = TerminalSurfaceOptions(backend: .inMemory(terminalSession))

        connect()
    }

    // MARK: - Connection lifecycle

    func reconnect() {
        guard connection == nil || phase != .connected else { return }
        eventTask?.cancel()
        connect()
    }

    /// Detach: WS close = zmx detach; the session keeps running server-side.
    func disconnect() {
        let connection = connection
        self.connection = nil
        connectionRef.set(nil)
        eventTask?.cancel()
        drainTask?.cancel()
        phase = .ended(.closedByClient)
        Task { await connection?.close() }
    }

    private func connect() {
        phase = .connecting
        let connection = AttachConnection(url: attachURL, headers: attachHeaders)
        self.connection = connection
        connectionRef.set(connection)

        eventTask = Task { [weak self] in
            let events = await connection.start()
            for await event in events {
                guard let self, !Task.isCancelled else { return }
                self.handle(event)
            }
        }
    }

    private func handle(_ event: AttachEvent) {
        switch event {
        case .connected:
            phase = .connected
            logger.debug("attached \(self.sessionID)")
            // zmx needs dimensions before it repaints; resend the last
            // known viewport as the mandatory initial resize.
            if let viewport = connectionRef.lastViewport {
                connection?.enqueueResize(cols: viewport.cols, rows: viewport.rows)
            }
        case .output(let data):
            deliver(data)
        case .ended(let reason):
            logger.debug("attach ended \(self.sessionID): \(String(describing: reason))")
            connection = nil
            connectionRef.set(nil)
            phase = .ended(reason)
        }
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
        drainTask = Task {
            // Wait (bounded) for the surface to come up; readViewportText()
            // is nil until Ghostty attaches the surface.
            var attempts = 0
            while !surfaceIsReady, attempts < 100 {
                if terminalSession.readViewportText() != nil { break }
                attempts += 1
                try? await Task.sleep(for: .milliseconds(50))
            }
            // Proceed either way — receive() safely drops without a surface,
            // and marking ready stops unbounded buffering.
            surfaceIsReady = true
            guard !Task.isCancelled else { return }
            while !pendingOutput.isEmpty {
                terminalSession.receive(pendingOutput.removeFirst())
            }
            drainTask = nil
        }
    }
}

/// Lock-guarded handle to the current connection so Ghostty's background
/// callbacks can enqueue synchronously across reconnects.
private final class ConnectionRef: @unchecked Sendable {
    private let lock = NSLock()
    private var connection: AttachConnection?
    private var viewport: (cols: UInt16, rows: UInt16)?

    var lastViewport: (cols: UInt16, rows: UInt16)? {
        lock.withLock { viewport }
    }

    func set(_ connection: AttachConnection?) {
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

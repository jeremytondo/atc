import Foundation
import Observation
import OSLog
import ATCAppServerAPI

private let logger = Logger(subsystem: "ElevenIdeas.atc", category: "appmodel")

/// A compact, value-semantic projection of every runtime's threads and
/// terminals, used by the window to reconcile store-driven removals.
struct WindowNavigationSnapshot: Equatable {
    struct Connection: Equatable {
        struct ThreadRecord: Equatable {
            let id: String
            let projectID: String
            let isArchived: Bool
            /// In the snapshot so a finish landing under the displayed thread
            /// re-runs reconciliation, which stamps it viewed (ATC-160).
            let isUnread: Bool
            /// In the snapshot so the thread notifier sees every trigger state
            /// change (ATC-185). Window reconciliation is idempotent, so the
            /// passes this adds cost nothing.
            let activityState: ThreadActivityState
        }

        struct TerminalRecord: Equatable {
            let id: String
            let projectID: String
            let threadID: String?
            let isLive: Bool
        }

        let id: UUID
        let threadsCurrent: Bool
        let terminalsCurrent: Bool
        let threads: [ThreadRecord]
        let terminals: [TerminalRecord]
    }

    let connections: [Connection]
}

/// Root domain model: owns the Connection list and one `ConnectionRuntime`
/// per Connection, plus the terminal-controller registry that keeps attach
/// WebSockets and Ghostty surfaces alive across navigation.
@Observable
final class AppModel {
    let connections: ConnectionsStore
    private(set) var runtimes: [ConnectionRuntime] = []

    /// Live terminal attaches by composite ref. Connections and surfaces
    /// stay alive here while the user switches around the sidebar, bounded
    /// by `attachmentBudget`.
    private(set) var terminals: [TerminalRef: TerminalSessionController] = [:]

    /// Maximum simultaneously attached terminals (WebSocket + Ghostty
    /// surface each). Not user-facing configuration. The visible terminal
    /// supplied by the window is never evicted, even if that means
    /// temporarily exceeding the budget.
    let attachmentBudget: Int

    /// LRU order over `terminals` keys, least-recently-used first.
    private var attachOrder: [TerminalRef] = []

    /// The app's one thread notifier: app-level so two open windows produce
    /// one banner, not two. Silent unless the app supplies the system one.
    private let threadNotifier: ThreadNotifier

    private let clientFactory: (ConnectionRecord, URL) -> any APIProtocol
    private let terminalControllerFactory: (String, ConnectionRuntime) -> TerminalSessionController
    private let terminalRecoveryMonitor: TerminalRecoveryMonitor
    private let eventStreamFactory: ConnectionRuntime.EventStreamFactory?

    /// The default (real Keychain-backed) construction path defers loading
    /// and runtime building to `start()` so launch never blocks the first
    /// frame on file or Keychain I/O (ATC-168 M4); an injected store is the
    /// test seam and is already loaded, so init builds runtimes directly.
    @ObservationIgnored private var needsDeferredStart = false
    /// False until the deferred launch load resolves (or immediately on the
    /// injected-store path): empty-state UI must not flash while the real
    /// Connection list is still hydrating.
    private(set) var hasStarted = false
    /// Window states registered for model-driven reconciliation (weak: a
    /// closed window's state must deallocate).
    @ObservationIgnored private var windowReconcilers: [WeakWindowState] = []
    @ObservationIgnored private var lastNavigationSnapshot: WindowNavigationSnapshot?
    /// A clicked banner whose thread cannot be opened yet. macOS delivers a
    /// click that launched the app before any window registers or any store
    /// has loaded, so the ref waits for reconciliation rather than being
    /// dropped on the floor. One slot: a newer click supersedes an older one.
    @ObservationIgnored private var pendingNotificationThread: ThreadRef?

    private struct WeakWindowState {
        weak var value: WindowState?
    }

    init(
        connections: ConnectionsStore? = nil,
        clientFactory: ((ConnectionRecord, URL) -> any APIProtocol)? = nil,
        terminalControllerFactory: ((String, ConnectionRuntime) -> TerminalSessionController)? = nil,
        terminalRecoveryMonitor: TerminalRecoveryMonitor? = nil,
        eventStreamFactory: ConnectionRuntime.EventStreamFactory? = nil,
        threadNotifier: ThreadNotifier? = nil,
        attachmentBudget: Int = 12
    ) {
        self.attachmentBudget = attachmentBudget
        self.threadNotifier = threadNotifier ?? ThreadNotifier()
        if let connections {
            self.connections = connections
            hasStarted = true
        } else {
            self.connections = ConnectionsStore(loadingDeferred: true)
            needsDeferredStart = true
        }
        self.clientFactory = clientFactory ?? { record, url in
            ConnectionClient.make(baseURL: url, token: record.token)
        }
        self.terminalControllerFactory = terminalControllerFactory ?? { terminalID, runtime in
            TerminalSessionController(
                terminalID: terminalID,
                endpoint: AttachEndpoint(
                    baseURL: runtime.baseURL,
                    headers: runtime.transportHeaders
                ),
                checkLive: { [weak runtime] in
                    await runtime?.terminals.checkLive(id: terminalID) ?? nil
                }
            )
        }
        self.terminalRecoveryMonitor = terminalRecoveryMonitor ?? TerminalRecoveryMonitor()
        self.eventStreamFactory = eventStreamFactory
        if !needsDeferredStart {
            for record in self.connections.connections {
                if let runtime = makeRuntime(record) {
                    runtimes.append(runtime)
                }
            }
        }
        self.terminalRecoveryMonitor.onRecovery = { [weak self] in
            self?.recoverTerminalsAfterInterruption()
        }
        self.terminalRecoveryMonitor.start()
        self.threadNotifier.onOpenThread = { [weak self] ref in
            self?.pendingNotificationThread = ref
            self?.openPendingNotificationThread()
        }
        observeNavigationChanges()
    }

    /// The launch path's second half (see `needsDeferredStart`): loads the
    /// persisted Connection list (Keychain hydration included) and builds
    /// the runtimes. Idempotent; called from the window root's task AND the
    /// Settings scene's, so reaching Settings before any window cannot
    /// observe (and then persist over) an unloaded list.
    func start() {
        guard needsDeferredStart else { return }
        needsDeferredStart = false
        connections.loadNow()
        for record in connections.connections {
            if let runtime = makeRuntime(record) {
                runtimes.append(runtime)
            }
        }
        hasStarted = true
    }

    // MARK: - Window reconciliation (ATC-168 M2)

    /// Reconciliation is a model concern: the model observes its own
    /// navigation snapshot and reconciles terminal lifecycle plus every
    /// registered window — instead of each window root observing every
    /// store array and re-evaluating its whole view tree per SSE refresh.
    /// Sound only while every observed store is MainActor-isolated: the
    /// window between the tracked read and re-arm has no interleaving.
    private func observeNavigationChanges() {
        let snapshot = withObservationTracking {
            windowNavigationSnapshot()
        } onChange: { [weak self] in
            Task { @MainActor [weak self] in
                self?.observeNavigationChanges()
            }
        }
        guard snapshot != lastNavigationSnapshot else { return }
        lastNavigationSnapshot = snapshot
        reconcileTerminalLifecycle()
        threadNotifier.reconcile(connections: runtimes.map(ThreadNotifier.ConnectionInput.init(runtime:)))
        for entry in windowReconcilers {
            entry.value?.reconcile(in: self)
        }
        openPendingNotificationThread()
    }

    func registerWindow(_ windowState: WindowState) {
        windowReconcilers.removeAll { $0.value == nil || $0.value === windowState }
        windowReconcilers.append(.init(value: windowState))
        windowState.reconcile(in: self)
        openPendingNotificationThread()
    }

    func unregisterWindow(_ windowState: WindowState) {
        windowReconcilers.removeAll { $0.value === windowState || $0.value == nil }
    }

    /// Keeps `windowReconcilers` ordered by key recency, so a banner click
    /// opens in the window the user last worked in — the one macOS raises on
    /// activation — not whichever happened to register last.
    func noteWindowKeyed(_ windowState: WindowState) {
        guard windowReconcilers.contains(where: { $0.value === windowState }) else { return }
        windowReconcilers.removeAll { $0.value === windowState || $0.value == nil }
        windowReconcilers.append(.init(value: windowState))
        openPendingNotificationThread()
    }

    // MARK: - Runtime access

    func runtime(id: UUID) -> ConnectionRuntime? {
        runtimes.first { $0.id == id }
    }

    /// Reachability of a Connection for status surfaces; `.unknown` when no
    /// runtime exists (e.g. a not-yet-saved draft).
    func reachability(of id: UUID) -> Reachability {
        runtime(id: id)?.reachability ?? .unknown
    }

    /// Network-backed mutations are only offered while the Connection's
    /// event stream is live and its latest combined refresh succeeded.
    func canMutate(connectionID: UUID) -> Bool {
        runtime(id: connectionID)?.reachability == .connected
    }

    func thread(for ref: ThreadRef) -> ATCThread? {
        runtime(id: ref.connectionID)?.threads.thread(id: ref.threadID)
    }

    func terminal(for ref: TerminalRef) -> Terminal? {
        runtime(id: ref.connectionID)?.terminals.terminal(id: ref.terminalID)
    }

    // MARK: - Projections

    /// The cross-Connection thread/project projection, built from every
    /// runtime. Surfaces ask for it rather than assembling the inputs
    /// themselves, so one place decides what a projection sees.
    func threadList(filter: ThreadFilter) -> ThreadListModel {
        ThreadListModel(
            inputs: runtimes.map(ThreadListModel.ConnectionInput.init(runtime:)),
            filter: filter
        )
    }

    var dashboard: DashboardModel {
        DashboardModel(inputs: runtimes.map(DashboardModel.ConnectionInput.init(runtime:)))
    }

    /// Projects are deliberately absent: a project deletion cascades
    /// server-side into thread/terminal deletions, which this snapshot
    /// does observe — reconciliation rides that cascade.
    func windowNavigationSnapshot() -> WindowNavigationSnapshot {
        WindowNavigationSnapshot(connections: runtimes.map { runtime in
            WindowNavigationSnapshot.Connection(
                id: runtime.id,
                threadsCurrent: runtime.threads.isResolved,
                terminalsCurrent: runtime.terminals.isResolved,
                threads: (runtime.threads.threads + runtime.threads.archivedThreads).map {
                    .init(
                        id: $0.id,
                        projectID: $0.projectId,
                        isArchived: $0.isArchived,
                        isUnread: $0.unread,
                        activityState: $0.activityState
                    )
                },
                terminals: runtime.terminals.terminals.map {
                    .init(id: $0.id, projectID: $0.projectId, threadID: $0.threadId, isLive: $0.isLive)
                }
            )
        })
    }

    /// Refreshes every Connection concurrently so one unreachable server
    /// doesn't delay the others.
    func refreshAll() async {
        await withTaskGroup { group in
            for runtime in runtimes {
                group.addTask { await runtime.refresh() }
            }
        }
    }

    // MARK: - Connection mutations

    /// Adds and starts a new Connection. Throws `ConnectionValidationError`.
    @discardableResult
    func addConnection(name: String, urlString: String, token: String) throws -> ConnectionRecord {
        let record = try connections.add(name: name, urlString: urlString, token: token)
        if let runtime = makeRuntime(record) {
            runtimes.append(runtime)
        }
        return record
    }

    /// Whether saving these draft values would rebuild the runtime (URL or
    /// token change). The Settings UI confirms first when terminals are live.
    func wouldRebuildConnection(id: UUID, urlString: String, token: String) -> Bool {
        guard let runtime = runtime(id: id) else { return false }
        let normalized = ConnectionURL.normalize(urlString) ?? urlString
        return normalized != runtime.record.urlString || token != runtime.record.token
    }

    /// Whether any terminal on this Connection has a live attach. Retained
    /// (ended) controllers don't count — editing the Connection would only
    /// drop history, not sever a running WebSocket.
    func hasLiveTerminals(connectionID: UUID) -> Bool {
        terminals.contains { ref, controller in
            ref.connectionID == connectionID && controller.isActivelyAttached
        }
    }

    /// Refs whose terminals have a live attach, for connection indicators.
    /// `terminals.keys` would also include ended controllers kept for
    /// scrollback.
    var activelyAttachedRefs: Set<TerminalRef> {
        Set(terminals.filter { $0.value.isActivelyAttached }.keys)
    }

    /// Saves an edit. Name-only changes update the record in place; URL or
    /// token changes tear down and rebuild that Connection's runtime (new
    /// client, fresh stores, terminals disconnected). Other Connections are
    /// untouched. Throws `ConnectionValidationError`.
    func updateConnection(id: UUID, name: String, urlString: String, token: String) throws {
        let rebuild = wouldRebuildConnection(id: id, urlString: urlString, token: token)
        try connections.update(id: id, name: name, urlString: urlString, token: token)
        guard let record = connections.connections.first(where: { $0.id == id }) else { return }
        guard let index = runtimes.firstIndex(where: { $0.id == id }) else {
            // A record whose runtime was skipped at launch (corrupted URL)
            // becomes usable again the moment an edit makes it valid.
            if let runtime = makeRuntime(record) {
                runtimes.append(runtime)
            }
            return
        }
        if rebuild, let runtime = makeRuntime(record) {
            teardown(runtimes[index])
            runtimes[index] = runtime
        } else {
            runtimes[index].updateRecord(record)
        }
    }

    /// Deletes a Connection locally and disconnects its terminals. Window
    /// navigation references are reconciled by `WindowState`.
    func removeConnection(id: UUID) {
        connections.remove(id: id)
        guard let index = runtimes.firstIndex(where: { $0.id == id }) else { return }
        teardown(runtimes[index])
        runtimes.remove(at: index)
    }

    // MARK: - Thread and terminal opening

    /// Idempotently opens the thread's TUI terminal on the server, then
    /// attaches (or reuses) its controller. Returns the terminal ref the
    /// window should display.
    @discardableResult
    func openThread(_ ref: ThreadRef, retentionContext: TerminalRetentionContext = .empty) async throws -> TerminalRef {
        // Every thread open funnels through here, so any open supersedes a
        // parked banner click: once the user navigates on their own, a click
        // still waiting on an unreachable server must not replay later and
        // yank their selection. (The click's own open passes harmlessly —
        // the slot was cleared when it was honored.)
        pendingNotificationThread = nil
        guard let runtime = runtime(id: ref.connectionID) else {
            throw AppServerUnavailable()
        }
        let terminal = try await runtime.threads.openTerminal(threadID: ref.threadID)
        runtime.terminals.merge(terminal)
        let terminalRef = TerminalRef(connectionID: ref.connectionID, terminalID: terminal.id)
        attachIfNeeded(to: terminal, connectionID: ref.connectionID, retentionContext: retentionContext)
        return terminalRef
    }

    /// Stamps the thread viewed on the server; the merged response clears the
    /// unread indicator everywhere (no optimistic write). Failures are
    /// deliberately swallowed: the indicator simply stays until the next view.
    func markThreadViewed(_ ref: ThreadRef) async {
        guard let runtime = runtime(id: ref.connectionID) else { return }
        _ = try? await runtime.threads.markViewed(id: ref.threadID)
    }

    /// Attaches a live terminal's controller, or reuses the retained one.
    func attachIfNeeded(
        to terminal: Terminal,
        connectionID: UUID,
        retentionContext: TerminalRetentionContext = .empty
    ) {
        let ref = TerminalRef(connectionID: connectionID, terminalID: terminal.id)
        if terminals[ref] != nil {
            markRecentlyUsed(ref)
            return
        }
        guard terminal.isLive, let runtime = runtime(id: connectionID) else { return }
        let controller = terminalControllerFactory(terminal.id, runtime)
        controller.onTerminalEnded = { [weak self] in
            self?.reconcileEndedTerminal(ref)
        }
        terminals[ref] = controller
        markRecentlyUsed(ref)
        evictOverBudget(retentionContext: retentionContext)
    }

    func touchTerminal(_ ref: TerminalRef) {
        guard terminals[ref] != nil else { return }
        markRecentlyUsed(ref)
    }

    /// Tears the controller down and releases its surface — eviction and
    /// Connection teardown, not a lifecycle judgment about the terminal.
    func disconnectTerminal(ref: TerminalRef) {
        terminals[ref]?.disconnect()
        terminals.removeValue(forKey: ref)
        attachOrder.removeAll { $0 == ref }
    }

    /// Wake and path recovery are app-wide signals. A controller decides
    /// whether it is still expected to be live; ended terminals and explicit
    /// disconnects therefore remain stopped.
    func recoverTerminalsAfterInterruption() {
        for controller in terminals.values {
            controller.recoverAfterInterruption()
        }
    }

    /// Stops interaction for terminals the latest successful refresh says
    /// are ended, keeping the controller and its surface registered — the
    /// final frame stays visible under the relaunch affordance, and only
    /// LRU eviction or teardown releases it. A terminal deleted outright is
    /// fully disconnected. Failed refreshes are deliberately ignored so
    /// connection loss never manufactures a lifecycle transition.
    func reconcileTerminalLifecycle() {
        for ref in Array(terminals.keys) {
            guard let runtime = runtime(id: ref.connectionID),
                  runtime.terminals.isResolved
            else { continue }
            guard let terminal = runtime.terminals.terminal(id: ref.terminalID) else {
                disconnectTerminal(ref: ref)
                continue
            }
            if !terminal.isLive {
                terminals[ref]?.confirmEnded()
            }
        }
    }

    // MARK: - Attachment budget

    /// Moves `ref` to the most-recently-used end of the LRU order. Called
    /// only for attached refs, so `attachOrder` stays a permutation of
    /// `terminals.keys` and never accumulates stale entries.
    private func markRecentlyUsed(_ ref: TerminalRef) {
        attachOrder.removeAll { $0 == ref }
        attachOrder.append(ref)
    }

    /// Evicts least-recently-used attaches past the budget through the
    /// standard disconnect path. The visible ref and the just-attached ref
    /// (the LRU tail) are skipped; if they alone exceed the budget, it is
    /// simply exceeded.
    private func evictOverBudget(retentionContext: TerminalRetentionContext) {
        guard terminals.count > attachmentBudget else { return }
        let newest = attachOrder.last
        for ref in attachOrder where terminals.count > attachmentBudget {
            if ref == newest || ref == retentionContext.visibleTerminal { continue }
            disconnectTerminal(ref: ref)
        }
    }

    /// A terminal ended mid-attach: refetch so the tombstone (and the
    /// thread's dropped link) lands, and tear down the dead controller's
    /// socket while keeping its surface for the final frame.
    private func reconcileEndedTerminal(_ ref: TerminalRef) {
        guard let runtime = runtime(id: ref.connectionID) else { return }
        Task {
            await runtime.terminals.refresh()
            await runtime.threads.refresh()
        }
    }

    // MARK: - Private

    /// Nil only for a corrupted persisted record (urlString that no longer
    /// parses): the Connection is skipped with a log instead of crashing at
    /// launch — records created through the store are always valid.
    private func makeRuntime(_ record: ConnectionRecord) -> ConnectionRuntime? {
        guard let url = URL(string: record.urlString) else {
            logger.error("skipping connection \(record.id) — unparseable URL \(record.urlString)")
            return nil
        }
        let runtime: ConnectionRuntime
        if let eventStreamFactory {
            runtime = ConnectionRuntime(
                record: record,
                client: clientFactory(record, url),
                baseURL: url,
                eventStreamFactory: eventStreamFactory
            )
        } else {
            runtime = ConnectionRuntime(record: record, client: clientFactory(record, url), baseURL: url)
        }
        runtime.start()
        return runtime
    }

    /// Honors a clicked banner once a window exists and its Connection has
    /// answered. `windowReconcilers` is ordered by key recency
    /// (`noteWindowKeyed`), so `.last` is the window the user last worked in.
    private func openPendingNotificationThread() {
        guard let ref = pendingNotificationThread,
              let window = windowReconcilers.compactMap(\.value).last,
              let runtime = runtime(id: ref.connectionID),
              runtime.threads.isResolved
        else { return }
        // A resolved store is the answer either way: open the thread, or drop
        // a click for one that no longer exists — or was archived, which
        // `openThread` refuses, so an archived hit must not consume the click
        // as if it had opened.
        pendingNotificationThread = nil
        guard let thread = runtime.threads.thread(id: ref.threadID), !thread.isArchived else { return }
        Task { await window.openThread(ref, in: self) }
    }

    private func teardown(_ runtime: ConnectionRuntime) {
        runtime.stop()
        threadNotifier.forget(connectionID: runtime.id)
        // A parked click for this connection can never resolve; without this
        // it would be re-checked on every reconcile forever.
        if pendingNotificationThread?.connectionID == runtime.id {
            pendingNotificationThread = nil
        }
        for ref in terminals.keys where ref.connectionID == runtime.id {
            disconnectTerminal(ref: ref)
        }
    }
}

/// The one client-side domain error: a mutation was attempted against a
/// Connection that no longer exists locally.
struct AppServerUnavailable: LocalizedError {
    var errorDescription: String? { "This connection no longer exists." }
}

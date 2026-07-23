import Foundation
import Observation
import OSLog
import ATCAPI

private let logger = Logger(subsystem: "ElevenIdeas.atc", category: "appmodel")

struct WindowNavigationSnapshot: Equatable {
    struct Connection: Equatable {
        struct WorkspaceRecord: Equatable {
            let id: String
            let projectID: String
        }

        struct SessionRecord: Equatable {
            let id: String
            let workspaceID: String?
            let status: SessionStatus
        }

        let id: UUID
        let workspacesCurrent: Bool
        let sessionsCurrent: Bool
        let workspaces: [WorkspaceRecord]
        let sessions: [SessionRecord]
    }

    let connections: [Connection]
}

/// Root domain model: owns the Connection list and one `ConnectionRuntime`
/// per Connection. Aggregation across Connections happens above the
/// per-runtime stores, in pure code (`DashboardGroups`).
@Observable
final class AppModel {
    let connections: ConnectionsStore
    let workspaceStartup: WorkspaceStartupStore
    private(set) var runtimes: [ConnectionRuntime] = []

    /// Live terminal attaches by composite ref. Connections and surfaces
    /// stay alive here while the user switches around the sidebar, bounded
    /// by `attachmentBudget`.
    private(set) var terminals: [SessionRef: TerminalSessionController] = [:]

    /// Maximum simultaneously attached terminals (WebSocket + Ghostty
    /// surface each). Not user-facing configuration. Pinned refs — the
    /// the selected Session and Active Workspace supplied by the window are
    /// never evicted, even if that means temporarily exceeding the budget.
    let attachmentBudget: Int

    /// LRU order over `terminals` keys, least-recently-used first.
    private var attachOrder: [SessionRef] = []

    /// Refs whose terminals were torn down through `disconnectTerminal`.
    /// Window reconciliation consults this so a Disconnect sticks instead
    /// of being silently undone on the next store change; any explicit
    /// attach clears the mark.
    private var detachedRefs: Set<SessionRef> = []

    private let clientFactory: (ConnectionRecord) -> any ATCClient
    private let terminalControllerFactory: (String, any ATCClient) -> TerminalSessionController
    private let terminalRecoveryMonitor: TerminalRecoveryMonitor

    init(
        connections: ConnectionsStore? = nil,
        clientFactory: ((ConnectionRecord) -> any ATCClient)? = nil,
        terminalControllerFactory: ((String, any ATCClient) -> TerminalSessionController)? = nil,
        terminalRecoveryMonitor: TerminalRecoveryMonitor? = nil,
        workspaceStartupDefaults: UserDefaults = .standard,
        attachmentBudget: Int = 12
    ) {
        self.attachmentBudget = attachmentBudget
        self.connections = connections ?? ConnectionsStore()
        self.workspaceStartup = WorkspaceStartupStore(defaults: workspaceStartupDefaults)
        self.clientFactory = clientFactory ?? { record in
            // makeRuntime rejects records whose urlString doesn't parse, so
            // the unwrap here can't be reached with a corrupted record.
            let url = URL(string: record.urlString)!
            return HTTPATCClient(server: ATCServer(baseURL: url, token: record.token))
        }
        self.terminalControllerFactory = terminalControllerFactory ?? { sessionID, client in
            TerminalSessionController(sessionID: sessionID, client: client)
        }
        self.terminalRecoveryMonitor = terminalRecoveryMonitor ?? TerminalRecoveryMonitor()
        for record in self.connections.connections {
            if let runtime = makeRuntime(record) {
                runtimes.append(runtime)
            }
        }
        self.terminalRecoveryMonitor.onRecovery = { [weak self] in
            self?.recoverTerminalsAfterInterruption()
        }
        self.terminalRecoveryMonitor.start()
    }

    // MARK: - Runtime access

    func runtime(id: UUID) -> ConnectionRuntime? {
        runtimes.first { $0.id == id }
    }

    /// Reachability of a Connection for status dots; `.unknown` when no
    /// runtime exists (e.g. a not-yet-saved draft).
    func reachability(of id: UUID) -> Reachability {
        runtime(id: id)?.reachability ?? .unknown
    }

    /// Network-backed mutations are only offered after the Connection's
    /// latest combined refresh succeeded. Unknown and unreachable runtimes
    /// are read-only until polling establishes a current model again.
    func canMutate(connectionID: UUID) -> Bool {
        runtime(id: connectionID)?.reachability == .connected
    }

    func canCreateWorkspace(in ref: ProjectRef) -> Bool {
        canMutate(connectionID: ref.connectionID)
            && runtime(id: ref.connectionID)?.projects.project(id: ref.projectID) != nil
    }

    func canStartSession(in ref: WorkspaceRef) -> Bool {
        canMutate(connectionID: ref.connectionID)
            && runtime(id: ref.connectionID)?.workspaces.workspace(id: ref.workspaceID) != nil
    }

    func session(for ref: SessionRef) -> Session? {
        runtime(id: ref.connectionID)?.sessions.session(id: ref.sessionID)
    }

    /// A compact, value-semantic projection used by the window to reconcile
    /// store-driven removals and delayed selection restoration.
    func windowNavigationSnapshot() -> WindowNavigationSnapshot {
        WindowNavigationSnapshot(connections: runtimes.map { runtime in
            WindowNavigationSnapshot.Connection(
                id: runtime.id,
                workspacesCurrent: runtime.workspaces.hasLoadedOnce
                    && runtime.workspaces.lastError == nil,
                sessionsCurrent: runtime.sessions.hasLoadedOnce
                    && runtime.sessions.lastError == nil,
                workspaces: runtime.workspaces.workspaces.map {
                    .init(id: $0.id, projectID: $0.projectId)
                },
                sessions: runtime.sessions.sessions.map {
                    .init(
                        id: $0.id,
                        workspaceID: $0.workspace?.id,
                        status: $0.status
                    )
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
    var activelyAttachedRefs: Set<SessionRef> {
        Set(terminals.filter { $0.value.isActivelyAttached }.keys)
    }

    /// Saves an edit. Name-only changes update the record in place; URL or
    /// token changes tear down and rebuild that Connection's runtime (new
    /// client, fresh stores, terminals disconnected). Other Connections are
    /// untouched. Throws `ConnectionValidationError`.
    func updateConnection(id: UUID, name: String, urlString: String, token: String) throws {
        let rebuild = wouldRebuildConnection(id: id, urlString: urlString, token: token)
        try connections.update(id: id, name: name, urlString: urlString, token: token)
        guard let record = connections.connections.first(where: { $0.id == id }),
              let index = runtimes.firstIndex(where: { $0.id == id }) else { return }
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
        workspaceStartup.removeConnection(connectionID: id)
        guard let index = runtimes.firstIndex(where: { $0.id == id }) else { return }
        teardown(runtimes[index])
        runtimes.remove(at: index)
    }

    /// Deletes a Project on its server, then removes its local startup
    /// override only after the server mutation succeeds.
    func deleteProject(_ ref: ProjectRef) async throws {
        guard let runtime = runtime(id: ref.connectionID) else {
            throw ATCError.api(
                code: "connection_not_found",
                message: "This connection no longer exists.",
                sessionID: nil
            )
        }
        try await runtime.projects.delete(id: ref.projectID)
        workspaceStartup.removeProject(
            connectionID: ref.connectionID,
            projectID: ref.projectID
        )
    }

    // MARK: - Terminal registry

    func attachIfNeeded(
        to session: Session,
        connectionID: UUID,
        retentionContext: TerminalRetentionContext = .empty
    ) {
        let ref = SessionRef(connectionID: connectionID, sessionID: session.id)
        detachedRefs.remove(ref)
        if terminals[ref] != nil {
            markRecentlyUsed(ref)
            return
        }
        guard session.status == .live,
              let runtime = runtime(id: connectionID) else { return }
        let controller = terminalControllerFactory(session.id, runtime.client)
        controller.onSessionEnded = { [weak self] in
            self?.reconcileEndedSession(ref)
        }
        terminals[ref] = controller
        markRecentlyUsed(ref)
        evictOverBudget(retentionContext: retentionContext)
    }

    func touchTerminal(_ ref: SessionRef) {
        guard terminals[ref] != nil else { return }
        markRecentlyUsed(ref)
    }

    func disconnectTerminal(ref: SessionRef) {
        terminals[ref]?.disconnect()
        terminals.removeValue(forKey: ref)
        attachOrder.removeAll { $0 == ref }
        detachedRefs.insert(ref)
    }

    /// Whether this ref was disconnected and never explicitly re-attached.
    func isDetached(_ ref: SessionRef) -> Bool {
        detachedRefs.contains(ref)
    }

    /// Wake and path recovery are app-wide signals. A controller decides
    /// whether it is still expected to be live; ended sessions and explicit
    /// disconnects therefore remain stopped.
    func recoverTerminalsAfterInterruption() {
        for controller in terminals.values {
            controller.recoverAfterInterruption()
        }
    }

    /// Tears down interaction for sessions the latest successful poll says
    /// are Ended or deleted. Failed refreshes are deliberately ignored so
    /// connection loss never manufactures a lifecycle transition.
    func reconcileTerminalLifecycle() {
        for ref in Array(terminals.keys) {
            guard let runtime = runtime(id: ref.connectionID),
                  runtime.sessions.hasLoadedOnce,
                  runtime.sessions.lastError == nil
            else { continue }
            guard runtime.sessions.session(id: ref.sessionID)?.status == .live else {
                disconnectTerminal(ref: ref)
                continue
            }
        }
    }

    /// Central stale-interaction reconciliation. Returns true when the
    /// error is the expected `session_ended` response and was consumed.
    @discardableResult
    func handleSessionInteractionError(_ error: any Error, connectionID: UUID) -> Bool {
        guard let error = error as? ATCError,
              error.apiCode == "session_ended",
              let sessionID = error.sessionID
        else { return false }
        reconcileEndedSession(SessionRef(connectionID: connectionID, sessionID: sessionID))
        return true
    }

    // MARK: - Attachment budget

    /// Moves `ref` to the most-recently-used end of the LRU order. Called
    /// only for attached refs, so `attachOrder` stays a permutation of
    /// `terminals.keys` and never accumulates stale entries.
    private func markRecentlyUsed(_ ref: SessionRef) {
        attachOrder.removeAll { $0 == ref }
        attachOrder.append(ref)
    }

    /// Evicts least-recently-used attaches past the budget through the
    /// standard disconnect path. Pinned refs and the just-attached ref
    /// (the LRU tail) are skipped; if they alone exceed the budget, it is
    /// simply exceeded.
    private func evictOverBudget(retentionContext: TerminalRetentionContext) {
        guard terminals.count > attachmentBudget else { return }
        // Ordering invariant: every terminals key was appended in
        // markRecentlyUsed, so attachOrder covers all candidates.
        let newest = attachOrder.last
        for ref in attachOrder where terminals.count > attachmentBudget {
            if ref == newest || isPinned(ref, retentionContext: retentionContext) { continue }
            disconnectTerminal(ref: ref)
        }
    }

    /// Navigation state is window-owned, so eviction receives a derived
    /// retention context instead of reading a duplicate mutable selection.
    private func isPinned(
        _ ref: SessionRef,
        retentionContext: TerminalRetentionContext
    ) -> Bool {
        if ref == retentionContext.selectedSession { return true }
        guard let activeWorkspace = retentionContext.activeWorkspace,
              activeWorkspace.connectionID == ref.connectionID
        else { return false }
        return session(for: ref)?.belongs(to: activeWorkspace) == true
    }

    private func reconcileEndedSession(_ ref: SessionRef) {
        runtime(id: ref.connectionID)?.sessions.reconcileEnded(id: ref.sessionID)
        if terminals[ref] != nil {
            disconnectTerminal(ref: ref)
        }
    }

    // MARK: - Private

    /// Nil only for a corrupted persisted record (urlString that no longer
    /// parses): the Connection is skipped with a log instead of crashing at
    /// launch — records created through the store are always valid.
    private func makeRuntime(_ record: ConnectionRecord) -> ConnectionRuntime? {
        guard URL(string: record.urlString) != nil else {
            logger.error("skipping connection \(record.id) — unparseable URL \(record.urlString)")
            return nil
        }
        let runtime = ConnectionRuntime(record: record, client: clientFactory(record))
        runtime.startPolling()
        return runtime
    }

    private func teardown(_ runtime: ConnectionRuntime) {
        runtime.stopPolling()
        for ref in terminals.keys where ref.connectionID == runtime.id {
            disconnectTerminal(ref: ref)
        }
        // A teardown disconnect is infrastructure, not user intent: a
        // rebuilt Connection may auto-reattach its selected session.
        detachedRefs = detachedRefs.filter { $0.connectionID != runtime.id }
    }
}

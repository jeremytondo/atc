import Foundation
import Observation
import OSLog
import ATCAPI

private let logger = Logger(subsystem: "ElevenIdeas.atc", category: "appmodel")

/// Root domain model: owns the Connection list and one `ConnectionRuntime`
/// per Connection. Aggregation across Connections happens above the
/// per-runtime stores, in pure code (`DashboardGroups`).
@Observable
final class AppModel {
    let connections: ConnectionsStore
    private(set) var runtimes: [ConnectionRuntime] = []

    /// Sidebar selection. Lives here (not in a view) so deleting a
    /// Connection can clear a selection that pointed into it. Selecting a
    /// session marks it most-recently-used for the attachment budget.
    var selection: SessionRef? {
        didSet {
            if let selection { markRecentlyUsed(selection) }
        }
    }

    /// The Workspace mounted in the window's shell (stays set while the
    /// Dashboard covers it). Lives here so the attachment budget can pin
    /// its sessions and so store-driven cleanup (a remote delete) has one
    /// owner; the window's route (Dashboard vs shell) stays view state.
    var openWorkspace: WorkspaceRef?

    /// Live terminal attaches by composite ref. Connections and surfaces
    /// stay alive here while the user switches around the sidebar, bounded
    /// by `attachmentBudget`.
    private(set) var terminals: [SessionRef: TerminalSessionController] = [:]

    /// Maximum simultaneously attached terminals (WebSocket + Ghostty
    /// surface each). Not user-facing configuration. Pinned refs — the
    /// current selection and the open Workspace's sessions — are never
    /// evicted, even if that means exceeding the budget until the
    /// Workspace closes (correctness over the cap).
    let attachmentBudget: Int

    /// LRU order over `terminals` keys, least-recently-used first.
    private var attachOrder: [SessionRef] = []

    private let clientFactory: (ConnectionRecord) -> any ATCClient
    private let terminalControllerFactory: (String, any ATCClient) -> TerminalSessionController
    private let terminalRecoveryMonitor: TerminalRecoveryMonitor

    init(
        connections: ConnectionsStore? = nil,
        clientFactory: ((ConnectionRecord) -> any ATCClient)? = nil,
        terminalControllerFactory: ((String, any ATCClient) -> TerminalSessionController)? = nil,
        terminalRecoveryMonitor: TerminalRecoveryMonitor? = nil,
        attachmentBudget: Int = 12
    ) {
        self.attachmentBudget = attachmentBudget
        self.connections = connections ?? ConnectionsStore()
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

    func session(for ref: SessionRef) -> Session? {
        runtime(id: ref.connectionID)?.sessions.session(id: ref.sessionID)
    }

    /// Whether the open Workspace still exists: false once its Connection
    /// is gone, or its workspaces store has loaded and no longer contains
    /// it (deleted via web/CLI). Drives the window back to the Dashboard.
    var openWorkspaceExists: Bool {
        guard let ref = openWorkspace,
              let runtime = runtime(id: ref.connectionID) else { return false }
        return !runtime.workspaces.hasLoadedOnce
            || runtime.workspaces.workspace(id: ref.workspaceID) != nil
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

    /// Deletes a Connection locally: stops polling, disconnects its
    /// terminals, clears a selection pointing into it. No server calls —
    /// Projects and Sessions remain on the atc server.
    func removeConnection(id: UUID) {
        connections.remove(id: id)
        guard let index = runtimes.firstIndex(where: { $0.id == id }) else { return }
        teardown(runtimes[index])
        runtimes.remove(at: index)
    }

    // MARK: - Terminal registry

    func attachIfNeeded(to session: Session, connectionID: UUID) {
        let ref = SessionRef(connectionID: connectionID, sessionID: session.id)
        markRecentlyUsed(ref)
        guard session.attachable,
              terminals[ref] == nil,
              let runtime = runtime(id: connectionID) else { return }
        terminals[ref] = terminalControllerFactory(session.id, runtime.client)
        evictOverBudget()
    }

    func disconnectTerminal(ref: SessionRef) {
        terminals[ref]?.disconnect()
        terminals.removeValue(forKey: ref)
        attachOrder.removeAll { $0 == ref }
    }

    /// Wake and path recovery are app-wide signals. A controller decides
    /// whether it is still expected to be live; ended sessions and explicit
    /// disconnects therefore remain stopped.
    func recoverTerminalsAfterInterruption() {
        for controller in terminals.values {
            controller.recoverAfterInterruption()
        }
    }

    // MARK: - Attachment budget

    /// Moves `ref` to the most-recently-used end of the LRU order (only
    /// while it's attached — the order tracks `terminals` keys).
    private func markRecentlyUsed(_ ref: SessionRef) {
        attachOrder.removeAll { $0 == ref }
        attachOrder.append(ref)
    }

    /// Evicts least-recently-used attaches past the budget through the
    /// standard disconnect path. Pinned refs and the just-attached ref
    /// (the LRU tail) are skipped; if they alone exceed the budget, it is
    /// simply exceeded.
    private func evictOverBudget() {
        guard terminals.count > attachmentBudget else { return }
        // Ordering invariant: every terminals key was appended in
        // markRecentlyUsed, so attachOrder covers all candidates.
        let newest = attachOrder.last
        for ref in attachOrder where terminals.count > attachmentBudget {
            if ref == newest || isPinned(ref) { continue }
            disconnectTerminal(ref: ref)
        }
    }

    /// The current selection and the open Workspace's sessions are never
    /// evicted. A session no longer in its store (deleted remotely) can't
    /// be resolved to a workspace and is therefore evictable.
    private func isPinned(_ ref: SessionRef) -> Bool {
        if ref == selection { return true }
        guard let openWorkspace, openWorkspace.connectionID == ref.connectionID else { return false }
        return session(for: ref)?.workspace?.id == openWorkspace.workspaceID
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
        if selection?.connectionID == runtime.id {
            selection = nil
        }
        if openWorkspace?.connectionID == runtime.id {
            openWorkspace = nil
        }
    }
}

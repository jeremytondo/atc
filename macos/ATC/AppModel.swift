import Foundation
import Observation
import OSLog
import ATCAPI

private let logger = Logger(subsystem: "ElevenIdeas.atc", category: "appmodel")

/// Root domain model: owns the Connection list and one `ConnectionRuntime`
/// per Connection. Aggregation across Connections happens above the
/// per-runtime stores, in pure code (`SidebarGroups`).
@Observable
final class AppModel {
    let connections: ConnectionsStore
    private(set) var runtimes: [ConnectionRuntime] = []

    /// Sidebar selection. Lives here (not in a view) so deleting a
    /// Connection can clear a selection that pointed into it.
    var selection: SessionRef?

    /// One archived filter for all runtimes' stores.
    var includeArchived = false {
        didSet {
            guard includeArchived != oldValue else { return }
            for runtime in runtimes {
                runtime.projects.includeArchived = includeArchived
                runtime.sessions.includeArchived = includeArchived
            }
        }
    }

    /// Live terminal attaches by composite ref. Connections and surfaces
    /// stay alive here while the user switches around the sidebar.
    private(set) var terminals: [SessionRef: TerminalSessionController] = [:]

    private let clientFactory: (ConnectionRecord) -> any ATCClient
    private let terminalControllerFactory: (String, any ATCClient) -> TerminalSessionController
    private let terminalRecoveryMonitor: TerminalRecoveryMonitor

    init(
        connections: ConnectionsStore? = nil,
        clientFactory: ((ConnectionRecord) -> any ATCClient)? = nil,
        terminalControllerFactory: ((String, any ATCClient) -> TerminalSessionController)? = nil,
        terminalRecoveryMonitor: TerminalRecoveryMonitor? = nil
    ) {
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
        guard session.attachable,
              terminals[ref] == nil,
              let runtime = runtime(id: connectionID) else { return }
        terminals[ref] = terminalControllerFactory(session.id, runtime.client)
    }

    func disconnectTerminal(ref: SessionRef) {
        terminals[ref]?.disconnect()
        terminals.removeValue(forKey: ref)
    }

    /// Wake and path recovery are app-wide signals. A controller decides
    /// whether it is still expected to be live; ended sessions and explicit
    /// disconnects therefore remain stopped.
    func recoverTerminalsAfterInterruption() {
        for controller in terminals.values {
            controller.recoverAfterInterruption()
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
        if includeArchived {
            runtime.projects.includeArchived = true
            runtime.sessions.includeArchived = true
        }
        runtime.startPolling()
        return runtime
    }

    private func teardown(_ runtime: ConnectionRuntime) {
        runtime.stopPolling()
        for ref in terminals.keys where ref.connectionID == runtime.id {
            terminals[ref]?.disconnect()
            terminals.removeValue(forKey: ref)
        }
        if selection?.connectionID == runtime.id {
            selection = nil
        }
    }
}

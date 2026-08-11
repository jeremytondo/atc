// Shared domain state for one Connection's terminals — live sessions and
// ended tombstones, thread-linked and standalone alike. Views filter what
// they show (the sidebar hides thread-linked terminals); this store carries
// the complete reconciled listing so attach flows and tombstone management
// share one source of truth. Refreshes are SSE-driven, never polled.

import ATCAppServerAPI
import Foundation
import Observation

@Observable
final class TerminalsStore {
    let client: any APIProtocol

    /// All terminals, newest first, as the server reconciles them.
    private(set) var terminals: [Terminal] = []
    private let refreshState = RefreshState(category: "terminals")

    var hasLoadedOnce: Bool { refreshState.hasLoadedOnce }
    var lastError: String? { refreshState.lastError }
    var isResolved: Bool { refreshState.isResolved }

    init(client: any APIProtocol) {
        self.client = client
    }

    func refresh() async {
        await refreshState.run {
            try await client.listTerminals(query: .init()).ok.body.json
        } apply: { fetched in
            terminals = fetched
        }
    }

    func terminal(id: String) -> Terminal? {
        terminals.first { $0.id == id }
    }

    /// Standalone (not thread-linked) terminals for one project, oldest
    /// first — the sidebar's Terminals section ordering.
    func standaloneTerminals(projectID: String) -> [Terminal] {
        terminals
            .filter { $0.projectId == projectID && $0.threadId == nil }
            .sorted { $0.createdAt < $1.createdAt }
    }

    /// Reconciled single-terminal read: the reconnect flow's authority on
    /// whether a terminal is still live. Returns nil when the server cannot
    /// be asked (the caller keeps retrying rather than guessing).
    func checkLive(id: String) async -> Bool? {
        do {
            let output = try await client.getTerminal(path: .init(terminalId: id))
            if case .notFound = output { return false }
            let terminal = try output.ok.body.json
            merge(terminal)
            return terminal.isLive
        } catch {
            return nil
        }
    }

    // MARK: - Mutations

    @discardableResult
    func create(_ request: Components.Schemas.CreateTerminalRequest) async throws -> Terminal {
        let terminal: Terminal
        switch try await client.createTerminal(body: .json(request)) {
        case .ok(let ok): terminal = try ok.body.json
        case .notFound(let failure): throw ServerError(try failure.body.json)
        case .unprocessableContent(let failure):
            let payload = try failure.body.json
            throw ServerError(anyOf: payload.value1, payload.value2, payload.value3)
        case .serviceUnavailable(let failure): throw ServerError(try failure.body.json)
        case .undocumented(statusCode: let status, _): throw ServerError.undocumented(status: status)
        }
        merge(terminal)
        return terminal
    }

    @discardableResult
    func rename(id: String, name: String) async throws -> Terminal {
        let terminal: Terminal
        switch try await client.updateTerminal(path: .init(terminalId: id), body: .json(.init(name: name))) {
        case .ok(let ok): terminal = try ok.body.json
        case .notFound(let failure): throw ServerError(try failure.body.json)
        case .undocumented(statusCode: let status, _): throw ServerError.undocumented(status: status)
        }
        merge(terminal)
        return terminal
    }

    func delete(id: String) async throws {
        switch try await client.deleteTerminal(path: .init(terminalId: id)) {
        case .noContent: break
        case .notFound(let failure): throw ServerError(try failure.body.json)
        case .serviceUnavailable(let failure): throw ServerError(try failure.body.json)
        case .undocumented(statusCode: let status, _): throw ServerError.undocumented(status: status)
        }
        refreshState.invalidateInFlight()
        terminals.removeAll { $0.id == id }
    }

    func merge(_ terminal: Terminal) {
        refreshState.invalidateInFlight()
        terminals.upsertNewestFirst(terminal)
    }
}

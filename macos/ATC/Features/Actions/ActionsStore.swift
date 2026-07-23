import Foundation
import Observation
import OSLog
import ATCAPI

private let logger = Logger(subsystem: "ElevenIdeas.atc", category: "actions")

/// Shared domain state for one Connection's server-wide actions.
@Observable
final class ActionsStore {
    let client: any ATCClient

    private(set) var actions: [ATCAction] = []
    private(set) var isLoading = false
    private(set) var hasLoadedOnce = false
    var lastError: String?

    /// Monotonic token: a slow in-flight refresh must not clobber the
    /// result of a newer one.
    private var refreshGeneration = 0
    private var followUpRefresh: Task<Void, Never>?

    init(client: any ATCClient) {
        self.client = client
    }

    func refresh() async {
        refreshGeneration += 1
        let generation = refreshGeneration
        isLoading = true
        defer {
            if generation == refreshGeneration {
                isLoading = false
                hasLoadedOnce = true
            }
        }
        do {
            let fetched = try await client.actions()
            guard generation == refreshGeneration else { return }
            actions = Self.sorted(fetched)
            lastError = nil
            logger.debug("refreshed \(self.actions.count) actions")
        } catch {
            guard generation == refreshGeneration else { return }
            lastError = error.localizedDescription
            logger.error("refresh failed: \(error)")
        }
    }

    @discardableResult
    func create(_ request: ActionCreate) async throws -> ATCAction {
        let action = try await client.createAction(request)
        merge(action)
        scheduleRefresh()
        logger.info("created action \(action.id, privacy: .public)")
        return action
    }

    @discardableResult
    func update(id: String, _ patch: ActionPatch) async throws -> ATCAction {
        let action = try await client.updateAction(id: id, patch)
        merge(action)
        scheduleRefresh()
        logger.info("updated action \(id, privacy: .public)")
        return action
    }

    @discardableResult
    func setEnabled(id: String, enabled: Bool) async throws -> ATCAction {
        try await update(id: id, ActionPatch(enabled: enabled))
    }

    func delete(id: String) async throws {
        try await client.deleteAction(id: id)
        actions.removeAll { $0.id == id }
        scheduleRefresh()
        logger.info("deleted action \(id, privacy: .public)")
    }

    func action(id: String) -> ATCAction? {
        actions.first { $0.id == id }
    }

    private func scheduleRefresh() {
        followUpRefresh?.cancel()
        followUpRefresh = Task { await refresh() }
    }

    private func merge(_ action: ATCAction) {
        if let index = actions.firstIndex(where: { $0.id == action.id }) {
            actions[index] = action
        } else {
            actions.append(action)
        }
        actions = Self.sorted(actions)
    }

    private static func sorted(_ actions: [ATCAction]) -> [ATCAction] {
        actions.sorted {
            let order = $0.name.localizedCaseInsensitiveCompare($1.name)
            return order == .orderedSame ? $0.id < $1.id : order == .orderedAscending
        }
    }
}

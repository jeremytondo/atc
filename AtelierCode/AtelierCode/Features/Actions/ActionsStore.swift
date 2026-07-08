import Foundation
import Observation
import OSLog
import CockpitAPI

private let logger = Logger(subsystem: "ElevenIdeas.AtelierCode", category: "actions")

/// Shared domain state for one Connection's action registry. Unlike
/// Projects/Sessions this isn't polled — the settings editor and pickers
/// refresh on demand, matching the server's read-through registry.
@Observable
final class ActionsStore {
    let client: any CockpitClient

    /// Sorted by display label for stable list presentation; list entries
    /// omit `command`/`args` (use `detail(name:)` for the full definition).
    private(set) var actions: [CockpitAction] = []
    private(set) var isLoading = false
    private(set) var hasLoadedOnce = false
    var lastError: String?

    /// Monotonic token: a slow in-flight refresh must not clobber the
    /// result of a newer one.
    private var refreshGeneration = 0

    init(client: any CockpitClient) {
        self.client = client
    }

    func refresh() async {
        refreshGeneration += 1
        let generation = refreshGeneration
        isLoading = true
        defer {
            isLoading = false
            hasLoadedOnce = true
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

    /// Full definition including `command`/`args`, which the list omits.
    func detail(name: String) async throws -> CockpitAction {
        try await client.action(name: name)
    }

    /// Mutations throw so the editor can show inline errors.
    @discardableResult
    func create(_ request: ActionWriteRequest) async throws -> CockpitAction {
        let action = try await client.createAction(request)
        merge(action)
        Task { await refresh() }
        return action
    }

    @discardableResult
    func update(name: String, _ request: ActionWriteRequest) async throws -> CockpitAction {
        let action = try await client.updateAction(name: name, request)
        merge(action)
        Task { await refresh() }
        return action
    }

    @discardableResult
    func setEnabled(name: String, enabled: Bool) async throws -> CockpitAction {
        let action = try await client.setActionEnabled(name: name, enabled: enabled)
        merge(action)
        Task { await refresh() }
        return action
    }

    /// Deletes a custom action or reverts a built-in override. Refreshes
    /// synchronously: the server's `{}` response doesn't say which happened,
    /// and a reverted built-in must reappear with its default definition.
    func delete(name: String) async throws {
        try await client.deleteAction(name: name)
        await refresh()
    }

    func action(name: String) -> CockpitAction? {
        actions.first { $0.name == name }
    }

    private func merge(_ action: CockpitAction) {
        if let index = actions.firstIndex(where: { $0.name == action.name }) {
            actions[index] = action
        } else {
            actions.append(action)
        }
        actions = Self.sorted(actions)
    }

    private static func sorted(_ actions: [CockpitAction]) -> [CockpitAction] {
        actions.sorted {
            let order = $0.displayLabel.localizedCaseInsensitiveCompare($1.displayLabel)
            return order == .orderedSame ? $0.name < $1.name : order == .orderedAscending
        }
    }
}

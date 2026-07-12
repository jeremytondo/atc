import Foundation
import ATCAPI

/// Persists each Workspace's last-selected session (`workspaceID →
/// sessionID`) in UserDefaults, so reopening a Workspace restores where the
/// user left off. Restoration is best-effort: a remembered session that no
/// longer exists (or moved) falls back to no selection.
struct WorkspaceSelectionMemory {
    private let defaults: UserDefaults
    private static let key = "workspaceSelections"

    init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
    }

    func sessionID(for workspaceID: String) -> String? {
        stored()[workspaceID]
    }

    func remember(sessionID: String, for workspaceID: String) {
        var map = stored()
        guard map[workspaceID] != sessionID else { return }
        map[workspaceID] = sessionID
        defaults.set(map, forKey: Self.key)
    }

    /// The selection to restore when opening a Workspace: the remembered
    /// session if it still exists in the store and still belongs to that
    /// Workspace (any lifecycle state); nil otherwise, which shows the
    /// Workspace empty state.
    func restoredSelection(for ref: WorkspaceRef, in sessions: [Session]) -> SessionRef? {
        guard let saved = sessionID(for: ref.workspaceID),
              let session = sessions.first(where: { $0.id == saved }),
              session.workspace?.id == ref.workspaceID
        else { return nil }
        return SessionRef(connectionID: ref.connectionID, sessionID: session.id)
    }

    func forget(workspaceID: String) {
        var map = stored()
        guard map.removeValue(forKey: workspaceID) != nil else { return }
        defaults.set(map, forKey: Self.key)
    }

    private func stored() -> [String: String] {
        defaults.dictionary(forKey: Self.key) as? [String: String] ?? [:]
    }
}

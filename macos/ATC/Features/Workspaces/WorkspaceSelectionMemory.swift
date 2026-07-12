import Foundation

/// Persists each Workspace's last-selected session (`workspaceID →
/// sessionID`) in UserDefaults, so reopening a Workspace restores where the
/// user left off. Restoration is best-effort: the caller checks the stored
/// session still exists in its store before selecting it.
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

    func forget(workspaceID: String) {
        var map = stored()
        guard map.removeValue(forKey: workspaceID) != nil else { return }
        defaults.set(map, forKey: Self.key)
    }

    private func stored() -> [String: String] {
        defaults.dictionary(forKey: Self.key) as? [String: String] ?? [:]
    }
}

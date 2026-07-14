import Foundation
@testable import ATC

extension WindowState {
    /// A WindowState whose selection memory lives in an ephemeral defaults
    /// suite. Tests must use this over `WindowState()`, whose default
    /// selection memory reads and writes the developer's real app defaults.
    static func ephemeral() -> WindowState {
        let suite = "WindowStateTests.\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        defaults.removePersistentDomain(forName: suite)
        return WindowState(
            selectionMemory: WorkspaceSelectionMemory(defaults: defaults)
        )
    }
}

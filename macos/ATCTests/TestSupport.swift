import AppKit
import Foundation
@testable import ATC

/// Pumps the main run loop for a fixed duration so hosted SwiftUI/AppKit
/// hierarchies can process layout, timers, and focus changes.
@MainActor
func pump(seconds: TimeInterval) {
    RunLoop.main.run(until: Date(timeIntervalSinceNow: seconds))
}

/// Pumps the main run loop in short slices until `condition` holds or
/// `timeout` elapses. Prefer this over a fixed pump when waiting on
/// asynchronous work such as focus transfer: it outlasts a slow CI machine
/// without slowing the common case.
@MainActor
func pump(until condition: () -> Bool, timeout: TimeInterval = 2) {
    let deadline = Date(timeIntervalSinceNow: timeout)
    while !condition(), Date() < deadline {
        RunLoop.main.run(until: Date(timeIntervalSinceNow: 0.02))
    }
}

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

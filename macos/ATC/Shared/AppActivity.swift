import AppKit

/// The one production answer to "is ATC frontmost?". `WindowState` (viewing
/// only counts when the user can see it) and `ThreadNotifier` (frontmost never
/// notifies) both default their test seams to `{ AppActivity.isActive() }`, so
/// the definition cannot drift between them. (Closure literals at the seams:
/// a bare function reference would pin `@MainActor` into the seam type.)
enum AppActivity {
    static func isActive() -> Bool { NSApplication.shared.isActive }
}

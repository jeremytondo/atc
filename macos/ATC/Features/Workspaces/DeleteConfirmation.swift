import Foundation

/// The one source of delete-confirmation copy, so every dialog names its
/// target and ends with the ADR 0008 sentence: files are never touched.
enum DeleteConfirmation {
    static let filesUntouched = "Files on disk are not touched."

    static func sessionMessage(displayName: String) -> String {
        "Delete Session “\(displayName)”? This stops the session if it is "
            + "running and removes its atc history. \(filesUntouched)"
    }

    /// Counts come from the local store at confirmation time.
    static func workspaceMessage(name: String, sessionCount: Int, activeCount: Int) -> String {
        var message = "Delete Workspace “\(name)” and its \(sessionCount) "
            + (sessionCount == 1 ? "session? " : "sessions? ")
        if activeCount > 0 {
            message += "\(activeCount) running "
                + (activeCount == 1 ? "session" : "sessions")
                + " will be stopped. "
        }
        message += filesUntouched
        return message
    }

    static func projectMessage(name: String) -> String {
        "Delete Project “\(name)”? \(filesUntouched)"
    }
}

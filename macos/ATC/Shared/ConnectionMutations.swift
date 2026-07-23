import SwiftUI

/// The one shared wrapper for Connection-backed mutations: refuses when the
/// Connection's model isn't current and routes failures into the caller's
/// alert state. Views must not hand-roll this guard.
extension AppModel {
    /// The one shared session delete flow (header, Session Ended state, and
    /// navigator context menu): remove the record, tear down the terminal,
    /// and return the window to the Workspace empty state when the deleted
    /// session was selected.
    func deleteSession(
        ref: SessionRef,
        windowState: WindowState,
        reporting actionError: Binding<String?>
    ) {
        guard let store = runtime(id: ref.connectionID)?.sessions else { return }
        run(on: ref.connectionID, reporting: actionError) {
            try await store.delete(id: ref.sessionID)
            self.disconnectTerminal(ref: ref)
            if windowState.selectedSession == ref {
                let workspaceExists = windowState.activeWorkspace.map {
                    self.runtime(id: $0.connectionID)?
                        .workspaces.workspace(id: $0.workspaceID) != nil
                } ?? false
                windowState.showWorkspaceEmpty(workspaceExists: workspaceExists)
            }
        }
    }

    func run(
        on connectionID: UUID,
        reporting actionError: Binding<String?>,
        _ operation: @escaping () async throws -> Void
    ) {
        Task {
            guard canMutate(connectionID: connectionID) else {
                actionError.wrappedValue = "The connection is unavailable."
                return
            }
            do {
                try await operation()
            } catch {
                if handleSessionInteractionError(error, connectionID: connectionID) {
                    actionError.wrappedValue = nil
                    return
                }
                actionError.wrappedValue = error.localizedDescription
            }
        }
    }
}

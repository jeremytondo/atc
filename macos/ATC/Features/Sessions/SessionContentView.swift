import SwiftUI
import ATCAPI

/// Content area whose TerminalPane is ALWAYS in the hierarchy, even with
/// nothing selected, so live surfaces and their WebSockets survive sidebar
/// navigation; non-terminal states draw an opaque cover over it.
struct SessionContentView: View {
    /// The Workspace empty state's actions; non-nil replaces the default
    /// "No Session Selected" cover when nothing is selected.
    struct EmptyStateActions {
        var newSession: () -> Void
        var newTerminal: () -> Void
        /// Mirrors command availability: false on an unreachable Connection.
        var creationEnabled = true
    }

    @Environment(AppModel.self) private var appModel
    let selectedRef: SessionRef?
    let selectedSession: Session?
    let terminalFocusRequest: UInt
    var emptyState: EmptyStateActions?

    @Environment(WindowState.self) private var windowState

    var body: some View {
        ZStack {
            TerminalPane(
                visibleRef: selectedRef,
                focusRequest: terminalFocusRequest
            )
            cover
        }
    }

    @ViewBuilder
    private var cover: some View {
        if let ref = selectedRef, let session = selectedSession {
            if session.status == .ended {
                SessionEndedView(sessionRef: ref, session: session)
                    .background(AppColors.canvas)
            } else if let controller = appModel.terminals[ref] {
                // Terminal visible; only the phase banner floats on top.
                TerminalStatusBanner(controller: controller) {
                    appModel.disconnectTerminal(ref: ref)
                }
            } else if session.status == .live {
                // Normally auto-attach creates the controller in the same
                // update; this cover is a defensive fallback for a live
                // session with no controller, so offer the way back in.
                ContentUnavailableView {
                    Label("Not Connected", systemImage: "cable.connector.slash")
                } description: {
                    Text("The session is running on the server.")
                } actions: {
                    Button("Connect") {
                        appModel.attachIfNeeded(
                            to: session,
                            connectionID: ref.connectionID,
                            retentionContext: windowState.retentionContext
                        )
                    }
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .background(AppColors.canvas)
            }
        } else if let emptyState {
            ContentUnavailableView {
                Label("Empty Workspace", systemImage: "square.on.square")
            } description: {
                Text("Start an agent session or a terminal in this workspace.")
            } actions: {
                Button("New Session") { emptyState.newSession() }
                    .disabled(!emptyState.creationEnabled)
                Button("New Terminal") { emptyState.newTerminal() }
                    .disabled(!emptyState.creationEnabled)
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .background(AppColors.canvas)
        } else {
            ContentUnavailableView(
                "No Session Selected",
                systemImage: "terminal",
                description: Text("Select a session in the sidebar.")
            )
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .background(AppColors.canvas)
        }
    }
}

/// Non-interactive presentation for an Ended session. Selection remains in
/// place so metadata and explicit record deletion stay available.
private struct SessionEndedView: View {
    @Environment(AppModel.self) private var appModel
    @Environment(WindowState.self) private var windowState
    let sessionRef: SessionRef
    let session: Session

    @State private var confirmDelete = false
    @State private var actionError: String?

    private var displayName: String {
        SessionKind.displayName(session: session)
    }

    var body: some View {
        ContentUnavailableView {
            Label("Session Ended", systemImage: "checkmark.circle")
        } description: {
            Text("This session is no longer interactive.")
        } actions: {
            Button("Delete Session", role: .destructive) {
                confirmDelete = true
            }
            .disabled(!appModel.canMutate(connectionID: sessionRef.connectionID))
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .confirmationDialog(
            "Delete Session “\(displayName)”?",
            isPresented: $confirmDelete
        ) {
            Button("Delete Session", role: .destructive) {
                deleteSession()
            }
            .disabled(!appModel.canMutate(connectionID: sessionRef.connectionID))
        } message: {
            Text(DeleteConfirmation.sessionMessage(
                displayName: displayName,
                status: session.status
            ))
        }
        .actionErrorAlert($actionError, title: "Session Action Failed")
    }

    private func deleteSession() {
        appModel.deleteSession(ref: sessionRef, windowState: windowState, reporting: $actionError)
    }
}

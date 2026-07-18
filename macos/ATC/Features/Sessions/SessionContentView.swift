import SwiftUI
import ATCAPI

/// Content area: compact header above the terminal stack. The TerminalPane
/// is ALWAYS in the hierarchy (even with nothing selected) so live surfaces
/// and their WebSockets survive any sidebar navigation; non-terminal states
/// draw an opaque cover over it.
struct SessionContentView: View {
    /// The Workspace empty state's actions; non-nil replaces the default
    /// "No Session Selected" cover when nothing is selected.
    struct EmptyStateActions {
        var newSession: () -> Void
        var newTerminal: () -> Void
        /// Mirrors command availability: false in an archived Workspace or
        /// on an unreachable Connection.
        var creationEnabled = true
    }

    @Environment(AppModel.self) private var appModel
    let selectedRef: SessionRef?
    let selectedSession: Session?
    let terminalFocusRequest: UInt
    var emptyState: EmptyStateActions?

    @Environment(WindowState.self) private var windowState

    var body: some View {
        VStack(spacing: 0) {
            if let ref = selectedRef, let session = selectedSession {
                SessionHeaderBar(
                    sessionRef: ref,
                    session: session,
                    showInspector: Binding(
                        get: { windowState.isInspectorPresented },
                        set: { windowState.isInspectorPresented = $0 }
                    )
                )
                Divider()
            }
            ZStack {
                TerminalPane(
                    visibleRef: selectedRef,
                    focusRequest: terminalFocusRequest
                )
                cover
            }
        }
    }

    @ViewBuilder
    private var cover: some View {
        if let ref = selectedRef, let session = selectedSession {
            if session.status == .ended {
                SessionEndedView(sessionRef: ref, session: session)
                    .background()
            } else if let controller = appModel.terminals[ref] {
                // Terminal visible; only the phase banner floats on top.
                TerminalStatusBanner(controller: controller) {
                    appModel.disconnectTerminal(ref: ref)
                }
            } else if session.status == .live {
                // Normally auto-attach creates the controller in the same
                // update; this cover only lingers after an explicit
                // Disconnect, so offer the way back in.
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
                .background()
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
            .background()
        } else {
            ContentUnavailableView(
                "No Session Selected",
                systemImage: "terminal",
                description: Text("Select a session in the sidebar.")
            )
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .background()
        }
    }
}

/// Compact header: name, action label, status, session actions.
struct SessionHeaderBar: View {
    @Environment(AppModel.self) private var appModel
    @Environment(WindowState.self) private var windowState
    let sessionRef: SessionRef
    let session: Session
    @Binding var showInspector: Bool

    @State private var confirmDelete = false
    @State private var actionError: String?

    private var isConnected: Bool {
        appModel.terminals[sessionRef]?.isActivelyAttached == true
    }

    private var canMutate: Bool {
        appModel.canMutate(connectionID: sessionRef.connectionID)
    }

    private var actions: [ATCAction] {
        appModel.runtime(id: sessionRef.connectionID)?.actions.actions ?? []
    }

    private var displayName: String {
        SessionKind.displayName(session: session, actions: actions)
    }

    var body: some View {
        HStack(spacing: Spacing.md) {
            StatusBadge(session: session, showLabel: true)
            Text(displayName)
                .font(.headline)
                .lineLimit(1)
            // What launched the session: "Claude", "Terminal", a custom
            // Action label.
            TagBadge(text: SessionKind.actionLabel(session: session, actions: actions))
            Text(session.workingDir)
                .font(.caption)
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .truncationMode(.head)
            Spacer()
            if session.status == .live && isConnected {
                Button("Disconnect", systemImage: "cable.connector.slash") {
                    appModel.disconnectTerminal(ref: sessionRef)
                }
                .help("Detach from the session (it keeps running)")
            }
            Button("Delete", systemImage: "trash") {
                confirmDelete = true
            }
            .disabled(!canMutate)
            .help("Delete this session")
            Button("Info", systemImage: "sidebar.trailing") {
                showInspector.toggle()
            }
            .help("Show session metadata")
        }
        // The trailing actions read as one toolbar row: icon-only,
        // borderless, discoverable through their .help strings.
        .buttonStyle(.borderless)
        .labelStyle(.iconOnly)
        .padding(.horizontal, Spacing.md)
        .padding(.vertical, Spacing.sm)
        .confirmationDialog(
            "Delete Session “\(displayName)”?",
            isPresented: $confirmDelete
        ) {
            Button("Delete Session", role: .destructive) {
                deleteSession()
            }
            .disabled(!canMutate)
        } message: {
            Text(DeleteConfirmation.sessionMessage(
                displayName: displayName,
                status: session.status
            ))
        }
        .actionErrorAlert($actionError, title: "Session Action Failed")
    }

    /// Failure leaves the session and surfaces the alert.
    private func deleteSession() {
        appModel.deleteSession(ref: sessionRef, windowState: windowState, reporting: $actionError)
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
        let actions = appModel.runtime(id: sessionRef.connectionID)?.actions.actions ?? []
        return SessionKind.displayName(session: session, actions: actions)
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

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
    var emptyState: EmptyStateActions?

    @State private var showInspector = false

    var body: some View {
        VStack(spacing: 0) {
            if let ref = selectedRef, let session = selectedSession {
                SessionHeaderBar(sessionRef: ref, session: session, showInspector: $showInspector)
                Divider()
            }
            ZStack {
                TerminalPane(visibleRef: selectedRef)
                cover
            }
        }
        .inspector(isPresented: $showInspector) {
            if let ref = selectedRef, let session = selectedSession,
               let client = appModel.runtime(id: ref.connectionID)?.client {
                SessionDetailView(session: session, client: client)
                    .inspectorColumnWidth(min: 260, ideal: 320)
            }
        }
    }

    @ViewBuilder
    private var cover: some View {
        if let ref = selectedRef, let session = selectedSession {
            if let controller = appModel.terminals[ref] {
                // Terminal visible; only the phase banner floats on top.
                TerminalStatusBanner(controller: controller) {
                    appModel.disconnectTerminal(ref: ref)
                }
            } else if session.attachable {
                // Normally auto-attach creates the controller in the same
                // update; this cover only lingers after an explicit
                // Disconnect, so offer the way back in.
                ContentUnavailableView {
                    Label("Not Connected", systemImage: "cable.connector.slash")
                } description: {
                    Text("The session is running on the server.")
                } actions: {
                    Button("Connect") {
                        appModel.attachIfNeeded(to: session, connectionID: ref.connectionID)
                    }
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .background()
            } else if let client = appModel.runtime(id: ref.connectionID)?.client {
                SessionDetailView(session: session, client: client)
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
    let sessionRef: SessionRef
    let session: Session
    @Binding var showInspector: Bool

    @State private var confirmStop = false
    @State private var confirmArchive = false
    @State private var confirmDelete = false
    @State private var actionError: String?

    private var isConnected: Bool {
        appModel.terminals[sessionRef]?.isActivelyAttached == true
    }

    private var canStop: Bool {
        session.status == .running || session.status == .starting
    }

    /// Mirror the server rule: archive only after the session ended.
    private var canArchive: Bool {
        !session.isArchived && (session.status == .terminated || session.status == .failed)
    }

    private var sessionsStore: SessionsStore? {
        appModel.runtime(id: sessionRef.connectionID)?.sessions
    }

    private var actions: [ATCAction] {
        appModel.runtime(id: sessionRef.connectionID)?.actions.actions ?? []
    }

    private var displayName: String {
        SessionKind.displayName(session: session, actions: actions)
    }

    var body: some View {
        HStack(spacing: 12) {
            StatusBadge(session: session, showLabel: true)
            Text(displayName)
                .font(.headline)
                .lineLimit(1)
            // What launched the session: "Claude", "Terminal", a custom
            // Action label.
            Text(SessionKind.actionLabel(session: session, actions: actions))
                .font(.caption)
                .foregroundStyle(.secondary)
                .padding(.horizontal, 6)
                .padding(.vertical, 2)
                .background(.quaternary.opacity(0.5), in: Capsule())
            Text(session.workingDir)
                .font(.caption)
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .truncationMode(.head)
            Spacer()
            if isConnected {
                Button("Disconnect", systemImage: "cable.connector.slash") {
                    appModel.disconnectTerminal(ref: sessionRef)
                }
                .help("Detach from the session (it keeps running)")
            }
            if canStop {
                Button("Stop", systemImage: "stop.circle") {
                    confirmStop = true
                }
                .help("Terminate this session")
            }
            if session.isArchived {
                Button("Unarchive", systemImage: "archivebox") {
                    Task { await run { try await sessionsStore?.unarchive(id: session.id) } }
                }
                .help("Unarchive this session")
            } else {
                Button("Archive", systemImage: "archivebox") {
                    confirmArchive = true
                }
                .disabled(!canArchive)
                .help(canArchive ? "Archive this session" : "Stop the session before archiving")
            }
            Button("Delete", systemImage: "trash") {
                confirmDelete = true
            }
            .help("Delete this session")
            if isConnected {
                Button("Info", systemImage: "sidebar.trailing") {
                    showInspector.toggle()
                }
                .help("Show session metadata")
            }
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
        .confirmationDialog(
            "Stop “\(displayName)”?",
            isPresented: $confirmStop
        ) {
            Button("Stop Session", role: .destructive) {
                Task { await run { try await sessionsStore?.terminate(id: session.id) } }
            }
        } message: {
            Text("The process will be terminated. The session record is kept until archived.")
        }
        .confirmationDialog(
            "Archive “\(displayName)”?",
            isPresented: $confirmArchive
        ) {
            Button("Archive Session") {
                Task { await run { try await sessionsStore?.archive(id: session.id) } }
            }
        } message: {
            Text("Archived sessions are hidden behind the archived filter.")
        }
        .confirmationDialog(
            "Delete Session “\(displayName)”?",
            isPresented: $confirmDelete
        ) {
            Button("Delete Session", role: .destructive) {
                Task { await deleteSession() }
            }
        } message: {
            Text(DeleteConfirmation.sessionMessage(displayName: displayName))
        }
        .alert("Session Action Failed", isPresented: Binding(
            get: { actionError != nil },
            set: { if !$0 { actionError = nil } }
        )) {
            Button("OK", role: .cancel) {}
        } message: {
            Text(actionError ?? "")
        }
    }

    /// Failure (stop error, 502) leaves the session and surfaces the alert.
    private func deleteSession() async {
        await run {
            try await sessionsStore?.delete(id: session.id)
            appModel.disconnectTerminal(ref: sessionRef)
            if appModel.selection == sessionRef {
                appModel.selection = nil
            }
        }
    }

    private func run(_ operation: () async throws -> Void) async {
        do {
            try await operation()
        } catch {
            actionError = error.localizedDescription
        }
    }
}

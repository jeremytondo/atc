import SwiftUI
import ATCAPI

/// Workspace-scoped Sessions and Terminals for the stable window sidebar.
/// Its data always derives from `WindowState.activeWorkspace`.
struct WorkspaceNavigatorView: View {
    @Environment(AppModel.self) private var appModel
    @Environment(WindowState.self) private var windowState

    @State private var actionError: String?
    @State private var deletingSession: Row?
    @State private var renameRequest: SessionRenameRequest?

    private struct Row: Identifiable {
        let ref: SessionRef
        let session: Session
        let title: String
        let kind: SessionKind
        var id: SessionRef { ref }
    }

    var body: some View {
        if workspaceRef == nil {
            ContentUnavailableView(
                "No Active Workspace",
                systemImage: "rectangle.stack",
                description: Text("Open a workspace to see its sessions and terminals.")
            )
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        } else {
            navigatorContent
        }
    }

    @ViewBuilder
    private var navigatorContent: some View {
        @Bindable var windowState = windowState
        let rows = sidebarRows()
        let agents = rows.filter { kind(of: $0.session) == .agent }
        let terminals = rows.filter { kind(of: $0.session) == .terminal }

        NavigatorList {
            if runtime?.reachability == .unreachable {
                Label("Disconnected", systemImage: "cable.connector.slash")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .navigatorListRow()
            }

            NavigatorDisclosureHeader(
                title: "Sessions",
                isExpanded: $windowState.isSessionsSectionExpanded,
                addHelp: "New session",
                isAddEnabled: canStartSession,
                onAdd: { windowState.startSessionKind = .agentSession }
            )
            if windowState.isSessionsSectionExpanded {
                sessionRows(agents, emptyText: "No sessions")
            }

            NavigatorDisclosureHeader(
                title: "Terminals",
                isExpanded: $windowState.isTerminalsSectionExpanded,
                addHelp: "New terminal",
                isAddEnabled: canStartSession,
                onAdd: { windowState.startSessionKind = .terminal }
            )
            if windowState.isTerminalsSectionExpanded {
                sessionRows(terminals, emptyText: "No terminals")
            }
        }
        .confirmationDialog(
            "Delete Session “\(deletingSession?.title ?? "")”?",
            isPresented: Binding(
                get: { deletingSession != nil },
                set: { if !$0 { deletingSession = nil } }
            )
        ) {
            Button("Delete Session", role: .destructive) {
                if let row = deletingSession { deleteSession(row) }
            }
            .disabled(!isConnected)
        } message: {
            if let row = deletingSession {
                Text(DeleteConfirmation.sessionMessage(
                    displayName: row.title,
                    status: row.session.status
                ))
            }
        }
        .alert(renameRequest?.dialogTitle ?? "Rename Session", isPresented: Binding(
            get: { renameRequest != nil },
            set: { if !$0 { renameRequest = nil } }
        )) {
            TextField("Name", text: renameDraft)
            Button("Rename") {
                renameSession()
            }
            .disabled(!(renameRequest?.canSubmit ?? false) || !isConnected)
            Button("Cancel", role: .cancel) {}
        }
        .actionErrorAlert($actionError, title: "Workspace Action Failed")
    }

    @ViewBuilder
    private func sessionRows(
        _ rows: [Row],
        emptyText: String
    ) -> some View {
        ForEach(rows) { row in
            NavigatorRow(
                isSelected: windowState.selectedSession == row.ref,
                action: { _ = windowState.selectSession(row.ref, in: appModel) }
            ) { _ in
                Text(row.title)
                    .lineLimit(1)
            } actions: {
                EmptyView()
            }
            .contextMenu { sessionMenu(row) }
        }
        if rows.isEmpty {
            Text(emptyText)
                .font(.caption)
                .foregroundStyle(.tertiary)
                .navigatorListRow()
        }
    }

    @ViewBuilder
    private func sessionMenu(_ row: Row) -> some View {
        Button("Rename…", systemImage: "pencil") {
            renameRequest = SessionRenameRequest(
                ref: row.ref,
                title: row.title,
                kind: row.kind
            )
        }
        .disabled(!isConnected)
        Divider()
        Button("Delete…", systemImage: "trash", role: .destructive) {
            deletingSession = row
        }
        .disabled(!isConnected)
    }

    private var workspaceRef: WorkspaceRef? { windowState.activeWorkspace }

    private var runtime: ConnectionRuntime? {
        workspaceRef.flatMap { appModel.runtime(id: $0.connectionID) }
    }

    private var canStartSession: Bool {
        windowState.canStartSession(in: appModel)
    }

    private var isConnected: Bool {
        runtime?.reachability == .connected
    }

    private func workspaceSessions() -> [Session] {
        guard let ref = workspaceRef, let runtime else { return [] }
        return runtime.sessions.sessions.filter { $0.belongs(to: ref) }
    }

    private func kind(of session: Session) -> SessionKind {
        SessionKind.classify(session: session, actions: runtime?.actions.actions ?? [])
    }

    /// Ordered sidebar rows: the Active Workspace's sessions, newest-first.
    private func sidebarRows() -> [Row] {
        guard let ref = workspaceRef else { return [] }
        let actions = runtime?.actions.actions ?? []
        return workspaceSessions()
            .sortedNewestFirst()
            .map { session in
                return Row(
                    ref: SessionRef(connectionID: ref.connectionID, sessionID: session.id),
                    session: session,
                    title: SessionKind.displayName(session: session, actions: actions),
                    kind: SessionKind.classify(session: session, actions: actions)
                )
            }
    }

    private func deleteSession(_ row: Row) {
        appModel.deleteSession(ref: row.ref, windowState: windowState, reporting: $actionError)
    }

    private var renameDraft: Binding<String> {
        Binding(
            get: { renameRequest?.draft ?? "" },
            set: { renameRequest?.draft = $0 }
        )
    }

    private func renameSession() {
        guard let request = renameRequest,
              let runtime = appModel.runtime(id: request.ref.connectionID)
        else { return }
        let store = runtime.sessions
        appModel.run(on: runtime.id, reporting: $actionError) {
            try await store.rename(id: request.ref.sessionID, name: request.normalizedName)
        }
    }
}

struct SessionRenameRequest: Equatable {
    let ref: SessionRef
    let kind: SessionKind
    var draft: String

    init(ref: SessionRef, title: String, kind: SessionKind) {
        self.ref = ref
        self.kind = kind
        self.draft = title
    }

    var dialogTitle: String {
        kind == .agent ? "Rename Session" : "Rename Terminal"
    }

    var normalizedName: String {
        draft.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    var canSubmit: Bool {
        !normalizedName.isEmpty
    }
}

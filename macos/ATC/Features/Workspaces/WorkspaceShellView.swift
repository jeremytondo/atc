import SwiftUI
import ATCAPI

/// Workspace-scoped Sessions and Terminals for the stable window sidebar.
/// Its data always derives from `WindowState.activeWorkspace`.
struct WorkspaceNavigatorView: View {
    @Environment(AppModel.self) private var appModel
    @Environment(WindowState.self) private var windowState

    @State private var actionError: String?
    @State private var deletingSession: Row?
    @State private var renamingSession: Row?
    @State private var renameDraft = ""

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
        .alert(renamePresentation.dialogTitle, isPresented: Binding(
            get: { renamingSession != nil },
            set: { if !$0 { renamingSession = nil } }
        )) {
            TextField("Name", text: $renameDraft)
            Button("Rename") {
                renameSession()
            }
            .disabled(!SessionRenamePresentation.canSubmit(renameDraft) || !isConnected)
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
            renameDraft = row.title
            renamingSession = row
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

    private var renamePresentation: SessionRenamePresentation {
        SessionRenamePresentation(kind: renamingSession?.kind ?? .agent)
    }

    private func renameSession() {
        guard let row = renamingSession, let runtime else { return }
        let name = SessionRenamePresentation.normalizedName(renameDraft)
        let store = runtime.sessions
        appModel.run(on: runtime.id, reporting: $actionError) {
            try await store.rename(id: row.session.id, name: name)
        }
    }
}

struct SessionRenamePresentation: Equatable {
    let kind: SessionKind

    var dialogTitle: String {
        kind == .agent ? "Rename Session" : "Rename Terminal"
    }

    static func normalizedName(_ draft: String) -> String {
        draft.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    static func canSubmit(_ draft: String) -> Bool {
        !normalizedName(draft).isEmpty
    }
}

/// Existing Workspace lifecycle actions, presented from the stable toolbar.
struct WorkspaceActionsMenu: View {
    @Environment(AppModel.self) private var appModel
    @Environment(WindowState.self) private var windowState

    @State private var renaming = false
    @State private var renameDraft = ""
    @State private var confirmDelete = false
    @State private var actionError: String?

    var body: some View {
        Menu {
            Button("New Workspace…", systemImage: "plus.square.on.square") {
                windowState.presentCreateWorkspace(in: appModel)
            }
            Button("Rename…", systemImage: "pencil") {
                renameDraft = workspace?.name ?? ""
                renaming = true
            }
            .disabled(!isConnected)
            Divider()
            if let workspace {
                if workspace.isArchived {
                    Button("Unarchive Workspace", systemImage: "archivebox") {
                        if let runtime {
                            let store = runtime.workspaces
                            appModel.run(on: runtime.id, reporting: $actionError) {
                                try await store.unarchive(id: workspace.id)
                            }
                        }
                    }
                    .disabled(!isConnected)
                } else {
                    Button("Archive Workspace", systemImage: "archivebox") {
                        if let runtime {
                            let store = runtime.workspaces
                            appModel.run(on: runtime.id, reporting: $actionError) {
                                try await store.archive(id: workspace.id)
                            }
                        }
                    }
                    .disabled(!isConnected || workspaceSessions.contains(where: \.isActive))
                }
            }
            Divider()
            Button("Delete Workspace…", systemImage: "trash", role: .destructive) {
                confirmDelete = true
            }
            .disabled(!isConnected)
        } label: {
            Label("Workspace", systemImage: "ellipsis.circle")
        }
        .help("Workspace actions")
        .alert("Rename Workspace", isPresented: $renaming) {
            TextField("Name", text: $renameDraft)
            Button("Rename") {
                if let workspace, let runtime {
                    let store = runtime.workspaces
                    let name = renameDraft.trimmingCharacters(in: .whitespaces)
                    appModel.run(on: runtime.id, reporting: $actionError) {
                        try await store.rename(id: workspace.id, name: name)
                    }
                }
            }
            .disabled(renameDraft.trimmingCharacters(in: .whitespaces).isEmpty || !isConnected)
            Button("Cancel", role: .cancel) {}
        }
        .confirmationDialog(
            "Delete Workspace “\(workspace?.name ?? "")”?",
            isPresented: $confirmDelete
        ) {
            Button("Delete Workspace", role: .destructive) { deleteWorkspace() }
                .disabled(!isConnected)
        } message: {
            if let workspace {
                Text(DeleteConfirmation.workspaceMessage(
                    name: workspace.name,
                    sessionCount: workspaceSessions.count,
                    activeCount: workspaceSessions.filter(\.isActive).count
                ))
            }
        }
        .actionErrorAlert($actionError, title: "Workspace Action Failed")
    }

    private var ref: WorkspaceRef? { windowState.activeWorkspace }
    private var runtime: ConnectionRuntime? {
        ref.flatMap { appModel.runtime(id: $0.connectionID) }
    }
    private var workspace: Workspace? {
        guard let ref else { return nil }
        return runtime?.workspaces.workspace(id: ref.workspaceID)
    }
    private var workspaceSessions: [Session] {
        guard let ref, let runtime else { return [] }
        return runtime.sessions.sessions.filter { $0.belongs(to: ref) }
    }
    private var isConnected: Bool { runtime?.reachability == .connected }

    private func deleteWorkspace() {
        guard let ref, let store = runtime?.workspaces else { return }
        appModel.run(on: ref.connectionID, reporting: $actionError) {
            try await store.delete(id: ref.workspaceID)
            windowState.forgetSelection(for: ref)
        }
    }
}

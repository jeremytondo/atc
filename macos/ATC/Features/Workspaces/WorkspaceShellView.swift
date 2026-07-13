import SwiftUI
import ATCAPI

/// Workspace-scoped Sessions and Terminals for the stable window sidebar.
/// Its data always derives from `WindowState.activeWorkspace`.
struct WorkspaceNavigatorView: View {
    @Environment(AppModel.self) private var appModel
    @Environment(WindowState.self) private var windowState

    @State private var showArchivedSessions = false
    @State private var actionError: String?
    @State private var deletingSession: Row?

    private struct Row: Identifiable {
        let ref: SessionRef
        let session: Session
        let title: String
        let caption: String
        var id: SessionRef { ref }
    }

    var body: some View {
        let rows = sidebarRows()
        let agents = rows.filter { kind(of: $0.session) == .agent }
        let terminals = rows.filter { kind(of: $0.session) == .terminal }

        List(selection: selectionBinding) {
            if runtime?.reachability == .unreachable {
                Label("Disconnected", systemImage: "cable.connector.slash")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            section(
                title: "Sessions",
                rows: agents,
                emptyText: "No sessions",
                newHelp: "New session",
                onNew: { windowState.startSessionKind = .agentSession }
            )
            section(
                title: "Terminals",
                rows: terminals,
                emptyText: "No terminals",
                newHelp: "New terminal",
                onNew: { windowState.startSessionKind = .terminal }
            )
        }
        .listStyle(.sidebar)
        .safeAreaInset(edge: .bottom, spacing: 0) {
            HStack {
                Toggle("Show Archived", isOn: $showArchivedSessions)
                    .toggleStyle(.checkbox)
                    .font(.caption)
                Spacer()
            }
            .padding(.horizontal, Spacing.md)
            .padding(.vertical, Spacing.xs)
            .background(.bar)
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
                Text(DeleteConfirmation.sessionMessage(displayName: row.title))
            }
        }
        .alert("Workspace Action Failed", isPresented: Binding(
            get: { actionError != nil },
            set: { if !$0 { actionError = nil } }
        )) {
            Button("OK", role: .cancel) {}
        } message: {
            Text(actionError ?? "")
        }
    }

    @ViewBuilder
    private func section(
        title: String,
        rows: [Row],
        emptyText: String,
        newHelp: String,
        onNew: @escaping () -> Void
    ) -> some View {
        Section {
            ForEach(rows) { row in
                SessionRowView(
                    session: row.session,
                    isConnected: appModel.activelyAttachedRefs.contains(row.ref),
                    title: row.title,
                    caption: row.caption
                )
                .tag(row.ref)
                .contextMenu { sessionMenu(row) }
            }
            if rows.isEmpty {
                Text(emptyText)
                    .font(.caption)
                    .foregroundStyle(.tertiary)
            }
        } header: {
            HStack {
                Text(title)
                Spacer()
                Button(action: onNew) { Image(systemName: "plus") }
                    .buttonStyle(.plain)
                    .foregroundStyle(.secondary)
                    .disabled(!canStartSession)
                    .help(newHelp)
            }
        }
    }

    @ViewBuilder
    private func sessionMenu(_ row: Row) -> some View {
        let session = row.session
        if session.isArchived {
            Button("Unarchive", systemImage: "archivebox") {
                if let store = runtime?.sessions {
                    run(on: row.ref.connectionID) {
                        try await store.unarchive(id: session.id)
                    }
                }
            }
            .disabled(!isConnected)
        } else if session.status == .terminated || session.status == .failed {
            Button("Archive", systemImage: "archivebox") {
                if let store = runtime?.sessions {
                    run(on: row.ref.connectionID) {
                        try await store.archive(id: session.id)
                    }
                }
            }
            .disabled(!isConnected)
        }
        Button("Delete…", systemImage: "trash", role: .destructive) {
            deletingSession = row
        }
        .disabled(!isConnected)
    }

    private var selectionBinding: Binding<SessionRef?> {
        Binding(
            get: { windowState.selectedSession },
            set: { ref in
                if let ref, ref != windowState.selectedSession {
                    _ = windowState.selectSession(ref, in: appModel)
                }
            }
        )
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
        return runtime.sessions.sessions.filter { $0.workspace?.id == ref.workspaceID }
    }

    private func kind(of session: Session) -> SessionKind {
        SessionKind.classify(session: session, actions: runtime?.actions.actions ?? [])
    }

    private func sidebarRows() -> [Row] {
        guard let ref = workspaceRef else { return [] }
        let actions = runtime?.actions.actions ?? []
        return workspaceSessions()
            .filter { showArchivedSessions || !$0.isArchived }
            .sorted {
                if $0.createdAt != $1.createdAt { return $0.createdAt > $1.createdAt }
                return $0.id < $1.id
            }
            .map { session in
                let title = SessionKind.displayName(session: session, actions: actions)
                let label = SessionKind.actionLabel(session: session, actions: actions)
                return Row(
                    ref: SessionRef(connectionID: ref.connectionID, sessionID: session.id),
                    session: session,
                    title: title,
                    caption: "\(label) · \(session.updatedAt.formatted(.relative(presentation: .named)))"
                )
            }
    }

    private func deleteSession(_ row: Row) {
        guard let store = runtime?.sessions else { return }
        run(on: row.ref.connectionID) {
            try await store.delete(id: row.session.id)
            appModel.disconnectTerminal(ref: row.ref)
            if windowState.selectedSession == row.ref {
                windowState.showWorkspaceEmpty()
            }
        }
    }

    private func run(
        on connectionID: UUID,
        _ operation: @escaping () async throws -> Void
    ) {
        Task {
            guard appModel.canMutate(connectionID: connectionID) else {
                actionError = "The connection is unavailable."
                return
            }
            do { try await operation() }
            catch { actionError = error.localizedDescription }
        }
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
                            run(on: runtime.id) {
                                try await store.unarchive(id: workspace.id)
                            }
                        }
                    }
                    .disabled(!isConnected)
                } else {
                    Button("Archive Workspace", systemImage: "archivebox") {
                        if let runtime {
                            let store = runtime.workspaces
                            run(on: runtime.id) {
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
                    run(on: runtime.id) {
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
        .alert("Workspace Action Failed", isPresented: Binding(
            get: { actionError != nil },
            set: { if !$0 { actionError = nil } }
        )) {
            Button("OK", role: .cancel) {}
        } message: {
            Text(actionError ?? "")
        }
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
        return runtime.sessions.sessions.filter { $0.workspace?.id == ref.workspaceID }
    }
    private var isConnected: Bool { runtime?.reachability == .connected }

    private func deleteWorkspace() {
        guard let ref, let store = runtime?.workspaces else { return }
        run(on: ref.connectionID) {
            try await store.delete(id: ref.workspaceID)
            windowState.forgetSelection(for: ref)
        }
    }

    private func run(
        on connectionID: UUID,
        _ operation: @escaping () async throws -> Void
    ) {
        Task {
            guard appModel.canMutate(connectionID: connectionID) else {
                actionError = "The connection is unavailable."
                return
            }
            do { try await operation() }
            catch { actionError = error.localizedDescription }
        }
    }
}

extension Session {
    var isActive: Bool { status == .starting || status == .running }
}

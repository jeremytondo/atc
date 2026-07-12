import SwiftUI
import ATCAPI

/// The Workspace surface: a sidebar of the open Workspace's Sessions and
/// Terminals with the shared session content area. Mounted once a Workspace
/// is opened and kept mounted while the Dashboard covers it, so terminal
/// surfaces and their WebSockets survive Dashboard round-trips.
struct WorkspaceShellView: View {
    @Environment(AppModel.self) private var appModel
    @Environment(WindowState.self) private var windowState

    private let selectionMemory = WorkspaceSelectionMemory()

    @State private var searchText = ""
    @State private var showArchivedSessions = false
    @State private var renamingWorkspace = false
    @State private var renameDraft = ""
    @State private var confirmDeleteWorkspace = false
    @State private var actionError: String?
    @State private var deletingSession: Row?
    /// Set when the sessions store hadn't loaded at open time; the restore
    /// re-runs once data arrives.
    @State private var pendingSelectionRestore = false

    /// One sidebar row: a session joined against the action registry.
    private struct Row: Identifiable {
        let ref: SessionRef
        let session: Session
        let title: String
        let caption: String
        var id: SessionRef { ref }
    }

    var body: some View {
        @Bindable var appModel = appModel
        @Bindable var windowState = windowState
        let rows = sidebarRows()
        NavigationSplitView(columnVisibility: $windowState.columnVisibility) {
            sidebar(rows: rows)
                .navigationSplitViewColumnWidth(min: 220, ideal: 280)
        } detail: {
            SessionContentView(
                selectedRef: appModel.selection,
                selectedSession: selectedSession,
                emptyState: workspaceIsEmpty
                    ? SessionContentView.EmptyStateActions(
                        newSession: { windowState.startSessionKind = .agentSession },
                        newTerminal: { windowState.startSessionKind = .terminal },
                        creationEnabled: canStartSession
                    )
                    : nil
            )
        }
        .navigationTitle(workspace?.name ?? "atc")
        .navigationSubtitle(project?.name ?? "")
        .toolbar {
            // The Dashboard is a content cover, not a window; hide the
            // shell's toolbar while it is covered.
            if windowState.route == .workspace {
                ToolbarItem(placement: .navigation) {
                    Button {
                        windowState.showDashboard()
                    } label: {
                        Label("Dashboard", systemImage: "chevron.left")
                    }
                    .help("Back to the Dashboard")
                    .keyboardShortcut(.upArrow, modifiers: .command)
                }
                ToolbarItem(placement: .navigation) {
                    if let runtime {
                        ConnectionChip(
                            name: runtime.record.name,
                            reachability: runtime.reachability
                        )
                    }
                }
                ToolbarItemGroup {
                    workspaceMenu
                }
            }
        }
        .onChange(of: appModel.selection) {
            persistSelection()
            attachSelectedIfNeeded()
        }
        .onChange(of: selectedSession) { attachSelectedIfNeeded() }
        .onChange(of: appModel.openWorkspace, initial: true) { restoreSelection() }
        .onChange(of: sessionsLoadedOnce) {
            if pendingSelectionRestore { restoreSelection() }
        }
        .alert("Rename Workspace", isPresented: $renamingWorkspace) {
            TextField("Name", text: $renameDraft)
            Button("Rename") {
                if let workspace, let store = runtime?.workspaces {
                    let trimmed = renameDraft.trimmingCharacters(in: .whitespaces)
                    run { try await store.rename(id: workspace.id, name: trimmed) }
                }
            }
            .disabled(renameDraft.trimmingCharacters(in: .whitespaces).isEmpty)
            Button("Cancel", role: .cancel) {}
        }
        .confirmationDialog(
            "Delete Workspace “\(workspace?.name ?? "")”?",
            isPresented: $confirmDeleteWorkspace
        ) {
            Button("Delete Workspace", role: .destructive) { deleteOpenWorkspace() }
        } message: {
            if let workspace {
                let members = workspaceSessions()
                Text(DeleteConfirmation.workspaceMessage(
                    name: workspace.name,
                    sessionCount: members.count,
                    activeCount: members.filter(\.isActive).count
                ))
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
                if let row = deletingSession {
                    deleteSession(row)
                }
            }
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

    // MARK: - Sidebar

    @ViewBuilder
    private func sidebar(rows: [Row]) -> some View {
        @Bindable var appModel = appModel
        let agents = rows.filter { kind(of: $0.session) == .agent }
        let terminals = rows.filter { kind(of: $0.session) == .terminal }
        List(selection: $appModel.selection) {
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
        .searchable(text: $searchText, placement: .sidebar, prompt: "Search sessions")
        .safeAreaInset(edge: .bottom, spacing: 0) {
            HStack {
                Toggle("Show Archived", isOn: $showArchivedSessions)
                    .toggleStyle(.checkbox)
                    .font(.caption)
                Spacer()
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 6)
            .background(.bar)
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
                Button(action: onNew) {
                    Image(systemName: "plus")
                }
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
                    run { try await store.unarchive(id: session.id) }
                }
            }
        } else if session.status == .terminated || session.status == .failed {
            Button("Archive", systemImage: "archivebox") {
                if let store = runtime?.sessions {
                    run { try await store.archive(id: session.id) }
                }
            }
        }
        Button("Delete…", systemImage: "trash", role: .destructive) {
            deletingSession = row
        }
    }

    // MARK: - Workspace menu

    private var workspaceMenu: some View {
        Menu {
            Button("New Workspace…", systemImage: "plus.square.on.square") {
                windowState.presentCreateWorkspace(in: appModel)
            }
            Button("Rename…", systemImage: "pencil") {
                renameDraft = workspace?.name ?? ""
                renamingWorkspace = true
            }
            Divider()
            if let workspace {
                if workspace.isArchived {
                    Button("Unarchive Workspace", systemImage: "archivebox") {
                        if let store = runtime?.workspaces {
                            run { try await store.unarchive(id: workspace.id) }
                        }
                    }
                } else {
                    // Mirrors the server rule: no archiving with active
                    // sessions; the 409 remains the source of truth.
                    Button("Archive Workspace", systemImage: "archivebox") {
                        if let store = runtime?.workspaces {
                            run { try await store.archive(id: workspace.id) }
                        }
                    }
                    .disabled(workspaceSessions().contains(where: \.isActive))
                }
            }
            Divider()
            Button("Delete Workspace…", systemImage: "trash", role: .destructive) {
                confirmDeleteWorkspace = true
            }
        } label: {
            Label("Workspace", systemImage: "ellipsis.circle")
        }
        .help("Workspace actions")
    }

    // MARK: - Data

    private var workspaceRef: WorkspaceRef? { appModel.openWorkspace }

    private var runtime: ConnectionRuntime? {
        workspaceRef.flatMap { appModel.runtime(id: $0.connectionID) }
    }

    private var workspace: Workspace? {
        guard let ref = workspaceRef else { return nil }
        return runtime?.workspaces.workspace(id: ref.workspaceID)
    }

    private var project: Project? {
        guard let workspace else { return nil }
        return runtime?.projects.project(id: workspace.projectId)
    }

    private var selectedSession: Session? {
        appModel.selection.flatMap { appModel.session(for: $0) }
    }

    private var sessionsLoadedOnce: Bool {
        runtime?.sessions.hasLoadedOnce ?? false
    }

    private var canStartSession: Bool {
        windowState.canStartSession(in: appModel)
    }

    /// Every session of the open Workspace, unfiltered.
    private func workspaceSessions() -> [Session] {
        guard let ref = workspaceRef, let runtime else { return [] }
        return runtime.sessions.sessions.filter { $0.workspace?.id == ref.workspaceID }
    }

    /// No sessions at all (unfiltered) — drives the Workspace empty state.
    private var workspaceIsEmpty: Bool {
        workspaceRef != nil && workspaceSessions().isEmpty
    }

    private func kind(of session: Session) -> SessionKind {
        SessionKind.classify(session: session, actions: runtime?.actions.actions ?? [])
    }

    /// Filtered, ordered sidebar rows: the open Workspace's sessions,
    /// newest-first, archived behind the footer toggle, searched by title.
    private func sidebarRows() -> [Row] {
        guard let ref = workspaceRef else { return [] }
        let actions = runtime?.actions.actions ?? []
        return workspaceSessions()
            .filter { showArchivedSessions || !$0.isArchived }
            .sorted { $0.createdAt > $1.createdAt }
            .compactMap { session in
                let title = SessionKind.displayName(session: session, actions: actions)
                if !searchText.isEmpty,
                   !title.localizedCaseInsensitiveContains(searchText) {
                    return nil
                }
                let label = SessionKind.actionLabel(session: session, actions: actions)
                let caption = "\(label) · \(session.updatedAt.formatted(.relative(presentation: .named)))"
                return Row(
                    ref: SessionRef(connectionID: ref.connectionID, sessionID: session.id),
                    session: session,
                    title: title,
                    caption: caption
                )
            }
    }

    // MARK: - Selection restore

    /// Restores the Workspace's remembered session if it still exists in
    /// the store (any lifecycle state); otherwise clears the selection so
    /// the shell shows the Workspace empty state.
    private func restoreSelection() {
        guard let ref = workspaceRef else { return }
        guard sessionsLoadedOnce else {
            pendingSelectionRestore = true
            return
        }
        pendingSelectionRestore = false
        appModel.selection = selectionMemory.restoredSelection(
            for: ref,
            in: runtime?.sessions.sessions ?? []
        )
    }

    /// Persists `workspaceID → sessionID` on every selection change.
    private func persistSelection() {
        guard let ref = workspaceRef,
              let selection = appModel.selection,
              selection.connectionID == ref.connectionID,
              let session = appModel.session(for: selection),
              session.workspace?.id == ref.workspaceID
        else { return }
        selectionMemory.remember(sessionID: selection.sessionID, for: ref.workspaceID)
    }

    /// Selecting an attachable session auto-attaches — no explicit Connect
    /// step. Also fires when a selected starting session becomes attachable.
    private func attachSelectedIfNeeded() {
        if let ref = appModel.selection, let session = selectedSession, session.attachable {
            appModel.attachIfNeeded(to: session, connectionID: ref.connectionID)
        }
    }

    // MARK: - Actions

    /// Failure (stop error, 502) leaves the row and surfaces the alert.
    private func deleteSession(_ row: Row) {
        guard let store = runtime?.sessions else { return }
        run {
            try await store.delete(id: row.session.id)
            appModel.disconnectTerminal(ref: row.ref)
            if appModel.selection == row.ref {
                appModel.selection = nil
            }
        }
    }

    private func deleteOpenWorkspace() {
        guard let ref = workspaceRef, let store = runtime?.workspaces else { return }
        run {
            try await store.delete(id: ref.workspaceID)
            selectionMemory.forget(workspaceID: ref.workspaceID)
            // Deleting the open Workspace routes back to the Dashboard.
            windowState.handleOpenWorkspaceGone(in: appModel)
        }
    }

    private func run(_ operation: @escaping () async throws -> Void) {
        Task {
            do {
                try await operation()
            } catch {
                actionError = error.localizedDescription
            }
        }
    }
}

extension Session {
    /// Active means the server may still be running a process: `starting`
    /// or `running`.
    var isActive: Bool { status == .starting || status == .running }
}

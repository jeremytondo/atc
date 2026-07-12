import SwiftUI
import ATCAPI

/// The launch surface: every Connection's Projects and Workspaces, for
/// finding, creating, and opening Workspaces. Rendered as an opaque cover
/// over the (possibly mounted) Workspace shell; opening a Workspace routes
/// the window to the shell without tearing this down.
struct DashboardView: View {
    @Environment(AppModel.self) private var appModel
    /// Opens a Workspace in the shell.
    var onOpenWorkspace: (WorkspaceRef) -> Void
    /// Presents the create-Workspace sheet fixed to a Project.
    var onCreateWorkspace: (ProjectRef) -> Void
    var onCreateProject: () -> Void

    @State private var showArchived = false
    /// Keyboard focus for arrow-key navigation; Return opens it.
    @State private var focusedWorkspace: WorkspaceRef?
    @State private var renamingWorkspace: DashboardGroups.WorkspaceRow?
    @State private var renamingProject: DashboardGroups.ProjectCard?
    @State private var renameDraft = ""
    @State private var deletingWorkspace: DashboardGroups.WorkspaceRow?
    @State private var deletingProject: DashboardGroups.ProjectCard?
    @State private var actionError: String?

    var body: some View {
        // One pass over the runtimes per render; the List indexes into it.
        let groups = DashboardGroups(
            inputs: appModel.runtimes.map {
                DashboardGroups.ConnectionInput(
                    connection: $0.record,
                    projects: $0.projects.projects,
                    workspaces: $0.workspaces.workspaces,
                    sessions: $0.sessions.sessions
                )
            },
            showArchived: showArchived
        )
        List(selection: $focusedWorkspace) {
            ForEach(groups.sections) { section in
                Section {
                    ForEach(section.cards) { card in
                        projectCard(card, reachable: isReachable(section.connectionID))
                    }
                    if section.cards.isEmpty {
                        Text("No projects")
                            .font(.caption)
                            .foregroundStyle(.tertiary)
                    }
                } header: {
                    connectionHeader(section)
                }
            }
        }
        .onKeyPress(.return) {
            guard let ref = focusedWorkspace, isReachable(ref.connectionID) else {
                return .ignored
            }
            onOpenWorkspace(ref)
            return .handled
        }
        .safeAreaInset(edge: .top, spacing: 0) {
            HStack {
                Text("Workspaces")
                    .font(.headline)
                Spacer()
                Toggle("Show Archived", isOn: $showArchived)
                    .toggleStyle(.checkbox)
                Button("New Project…") { onCreateProject() }
                    .disabled(appModel.runtimes.isEmpty)
                Button {
                    Task { await appModel.refreshAll() }
                } label: {
                    Label("Refresh", systemImage: "arrow.clockwise")
                }
                .labelStyle(.iconOnly)
                .help("Refresh projects, workspaces, and sessions")
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 10)
            .background(.bar)
        }
        .overlay {
            if appModel.runtimes.isEmpty {
                noConnectionsState
            } else if groups.isEmpty && allLoadedOnce {
                emptyState
            }
        }
        .alert("Rename Workspace", isPresented: Binding(
            get: { renamingWorkspace != nil },
            set: { if !$0 { renamingWorkspace = nil } }
        )) {
            TextField("Name", text: $renameDraft)
            Button("Rename") {
                if let row = renamingWorkspace, let store = workspacesStore(for: row.ref.connectionID) {
                    let trimmed = renameDraft.trimmingCharacters(in: .whitespaces)
                    run { try await store.rename(id: row.workspace.id, name: trimmed) }
                }
            }
            .disabled(renameDraft.trimmingCharacters(in: .whitespaces).isEmpty)
            Button("Cancel", role: .cancel) {}
        }
        .alert("Rename Project", isPresented: Binding(
            get: { renamingProject != nil },
            set: { if !$0 { renamingProject = nil } }
        )) {
            TextField("Name", text: $renameDraft)
            Button("Rename") {
                if let card = renamingProject,
                   let store = appModel.runtime(id: card.ref.connectionID)?.projects {
                    let trimmed = renameDraft.trimmingCharacters(in: .whitespaces)
                    run { try await store.rename(id: card.project.id, name: trimmed) }
                }
            }
            .disabled(renameDraft.trimmingCharacters(in: .whitespaces).isEmpty)
            Button("Cancel", role: .cancel) {}
        }
        .confirmationDialog(
            "Delete Workspace “\(deletingWorkspace?.workspace.name ?? "")”?",
            isPresented: Binding(
                get: { deletingWorkspace != nil },
                set: { if !$0 { deletingWorkspace = nil } }
            )
        ) {
            Button("Delete Workspace", role: .destructive) {
                if let row = deletingWorkspace {
                    deleteWorkspace(row)
                }
            }
        } message: {
            if let row = deletingWorkspace {
                Text(DeleteConfirmation.workspaceMessage(
                    name: row.workspace.name,
                    sessionCount: row.sessionCount,
                    activeCount: row.activeSessionCount
                ))
            }
        }
        .confirmationDialog(
            "Delete Project “\(deletingProject?.project.name ?? "")”?",
            isPresented: Binding(
                get: { deletingProject != nil },
                set: { if !$0 { deletingProject = nil } }
            )
        ) {
            Button("Delete Project", role: .destructive) {
                if let card = deletingProject,
                   let store = appModel.runtime(id: card.ref.connectionID)?.projects {
                    run { try await store.delete(id: card.project.id) }
                }
            }
        } message: {
            if let card = deletingProject {
                Text(DeleteConfirmation.projectMessage(name: card.project.name))
            }
        }
        .alert("Action Failed", isPresented: Binding(
            get: { actionError != nil },
            set: { if !$0 { actionError = nil } }
        )) {
            Button("OK", role: .cancel) {}
        } message: {
            Text(actionError ?? "")
        }
    }

    // MARK: - Connection section

    @ViewBuilder
    private func connectionHeader(_ section: DashboardGroups.Section) -> some View {
        let reachability = appModel.reachability(of: section.connectionID)
        HStack(spacing: 6) {
            Circle()
                .fill(reachability.color)
                .frame(width: 7, height: 7)
            Text(section.connectionName)
            Text(section.contextLabel)
                .font(.caption)
                .foregroundStyle(.secondary)
            if reachability == .unreachable {
                Image(systemName: "cable.connector.slash")
                    .foregroundStyle(.secondary)
                Button("Retry") {
                    if let runtime = appModel.runtime(id: section.connectionID) {
                        Task { await runtime.refresh() }
                    }
                }
                .buttonStyle(.link)
                .font(.caption)
            }
            Spacer()
        }
    }

    // MARK: - Project card

    @ViewBuilder
    private func projectCard(_ card: DashboardGroups.ProjectCard, reachable: Bool) -> some View {
        let project = card.project
        Group {
            HStack(spacing: 6) {
                Label {
                    Text(project.name)
                        .lineLimit(1)
                } icon: {
                    Image(systemName: project.isArchived ? "archivebox" : "folder")
                }
                Text(project.workingDir)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .truncationMode(.head)
                Spacer(minLength: 4)
                if !project.isArchived {
                    Button {
                        onCreateWorkspace(card.ref)
                    } label: {
                        Image(systemName: "plus")
                    }
                    .buttonStyle(.plain)
                    .foregroundStyle(.secondary)
                    .disabled(!reachable)
                    .help("New workspace in \(project.name)")
                }
            }
            .opacity(project.isArchived ? 0.5 : 1)
            .contextMenu { projectMenu(card, reachable: reachable) }

            ForEach(card.rows) { row in
                workspaceRow(row, project: project, reachable: reachable)
            }
            if card.rows.isEmpty && !project.isArchived {
                // Quiet inline row, not a full empty-state panel.
                Button {
                    onCreateWorkspace(card.ref)
                } label: {
                    Label("New Workspace", systemImage: "plus")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                .buttonStyle(.plain)
                .padding(.leading, 24)
                .disabled(!reachable)
            }
        }
    }

    @ViewBuilder
    private func projectMenu(_ card: DashboardGroups.ProjectCard, reachable: Bool) -> some View {
        let project = card.project
        if !project.isArchived {
            Button("New Workspace…", systemImage: "plus") {
                onCreateWorkspace(card.ref)
            }
            .disabled(!reachable)
            Button("Rename…", systemImage: "pencil") {
                renameDraft = project.name
                renamingProject = card
            }
            Divider()
            // Mirrors the server rule: archive only once every Workspace
            // is archived. A stale view still gets the 409 via the alert.
            Button("Archive Project", systemImage: "archivebox") {
                if let store = appModel.runtime(id: card.ref.connectionID)?.projects {
                    run { try await store.archive(id: project.id) }
                }
            }
            .disabled(card.hasUnarchivedWorkspaces)
        } else {
            Button("Unarchive Project", systemImage: "archivebox") {
                if let store = appModel.runtime(id: card.ref.connectionID)?.projects {
                    run { try await store.unarchive(id: project.id) }
                }
            }
        }
        Divider()
        Button("Delete Project…", systemImage: "trash", role: .destructive) {
            deletingProject = card
        }
        .disabled(card.totalWorkspaceCount > 0)
        .help(card.totalWorkspaceCount > 0 ? "Delete all Workspaces first" : "")
    }

    // MARK: - Workspace row

    @ViewBuilder
    private func workspaceRow(
        _ row: DashboardGroups.WorkspaceRow,
        project: Project,
        reachable: Bool
    ) -> some View {
        let workspace = row.workspace
        HStack(spacing: 6) {
            Image(systemName: "square.on.square")
                .foregroundStyle(.secondary)
                .font(.caption)
            Text(workspace.name)
                .lineLimit(1)
            if workspace.isArchived {
                Text("Archived")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                    .padding(.horizontal, 5)
                    .padding(.vertical, 1)
                    .background(.quaternary.opacity(0.5), in: Capsule())
            }
            Spacer()
        }
        .padding(.leading, 20)
        .opacity(workspace.isArchived ? 0.5 : 1)
        .tag(row.ref)
        .contentShape(Rectangle())
        .onTapGesture {
            focusedWorkspace = row.ref
            if reachable { onOpenWorkspace(row.ref) }
        }
        .contextMenu {
            Button("Open", systemImage: "arrow.up.forward.square") {
                onOpenWorkspace(row.ref)
            }
            .disabled(!reachable)
            Button("Rename…", systemImage: "pencil") {
                renameDraft = workspace.name
                renamingWorkspace = row
            }
            Divider()
            if workspace.isArchived {
                Button("Unarchive", systemImage: "archivebox") {
                    if let store = workspacesStore(for: row.ref.connectionID) {
                        run { try await store.unarchive(id: workspace.id) }
                    }
                }
            } else {
                // Mirrors the server rule: no archiving with active sessions.
                Button("Archive", systemImage: "archivebox") {
                    if let store = workspacesStore(for: row.ref.connectionID) {
                        run { try await store.archive(id: workspace.id) }
                    }
                }
                .disabled(row.hasActiveSessions)
            }
            Divider()
            Button("Delete…", systemImage: "trash", role: .destructive) {
                deletingWorkspace = row
            }
        }
    }

    // MARK: - Empty states

    private var noConnectionsState: some View {
        ContentUnavailableView {
            Label("Add a Connection", systemImage: "network.slash")
        } description: {
            Text("Connect to an atc server in Settings to see its projects and workspaces here.")
        } actions: {
            SettingsLink {
                Text("Open Settings")
            }
        }
        .background()
    }

    private var emptyState: some View {
        ContentUnavailableView {
            Label("No Projects", systemImage: "folder")
        } description: {
            Text("Create a project to start working in a codebase.")
        } actions: {
            Button("New Project") { onCreateProject() }
        }
        .background()
    }

    private var allLoadedOnce: Bool {
        appModel.runtimes.allSatisfy {
            $0.projects.hasLoadedOnce && $0.workspaces.hasLoadedOnce
        }
    }

    // MARK: - Helpers

    private func isReachable(_ connectionID: UUID) -> Bool {
        appModel.reachability(of: connectionID) != .unreachable
    }

    private func workspacesStore(for connectionID: UUID) -> WorkspacesStore? {
        appModel.runtime(id: connectionID)?.workspaces
    }

    private func deleteWorkspace(_ row: DashboardGroups.WorkspaceRow) {
        run {
            guard let store = workspacesStore(for: row.ref.connectionID) else { return }
            try await store.delete(id: row.workspace.id)
            WorkspaceSelectionMemory().forget(workspaceID: row.workspace.id)
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

#Preview {
    DashboardView(onOpenWorkspace: { _ in }, onCreateWorkspace: { _ in }, onCreateProject: {})
        .environment(AppModel.preview())
        .preferredColorScheme(.dark)
}

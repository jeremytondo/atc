import AppKit
import SwiftUI
import ATCAPI

/// App-wide Project and Workspace management rendered as a main-content
/// destination inside the stable window split view.
struct DashboardView: View {
    @Environment(AppModel.self) private var appModel
    /// Opens a Workspace in the shell.
    var onOpenWorkspace: (WorkspaceRef) -> Void
    /// Presents the create-Workspace sheet fixed to a Project.
    var onCreateWorkspace: (ProjectRef) -> Void
    var onCreateProject: () -> Void

    @State private var showArchived = false
    @State private var renamingWorkspace: DashboardGroups.WorkspaceRow?
    @State private var renamingProject: DashboardGroups.ProjectCard?
    @State private var renameDraft = ""
    @State private var deletingWorkspace: DashboardGroups.WorkspaceRow?
    @State private var deletingProject: DashboardGroups.ProjectCard?
    @State private var actionError: String?

    var body: some View {
        // One pass over the runtimes per render; sections reuse the result.
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
        ScrollView {
            LazyVStack(alignment: .leading, spacing: 34) {
                ForEach(groups.sections) { section in
                    connectionSection(section)
                }
            }
            .frame(maxWidth: 1_280, alignment: .leading)
            .padding(.horizontal, 36)
            .padding(.vertical, 32)
            .frame(maxWidth: .infinity, alignment: .top)
        }
        .toolbar {
            ToolbarItemGroup(placement: .primaryAction) {
                Menu {
                    Toggle("Show Archived", isOn: $showArchived)
                    Divider()
                    Button("Refresh", systemImage: "arrow.clockwise") {
                        Task { await appModel.refreshAll() }
                    }
                } label: {
                    Label("Dashboard Options", systemImage: "ellipsis.circle")
                }
                .labelStyle(.iconOnly)
                .help("Dashboard options")

                Button {
                    onCreateProject()
                } label: {
                    Label("New Project", systemImage: "plus")
                }
                .labelStyle(.iconOnly)
                .disabled(!canCreateProject)
                .help("New project")
            }
        }
        .overlay {
            if appModel.runtimes.isEmpty {
                noConnectionsState
            } else if groups.totalProjectCount == 0 && allLoadedOnce {
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
            .disabled(
                renameDraft.trimmingCharacters(in: .whitespaces).isEmpty
                    || !(renamingWorkspace.map { canMutate($0.ref.connectionID) } ?? false)
            )
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
            .disabled(
                renameDraft.trimmingCharacters(in: .whitespaces).isEmpty
                    || !(renamingProject.map { canMutate($0.ref.connectionID) } ?? false)
            )
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
            .disabled(!(deletingWorkspace.map { canMutate($0.ref.connectionID) } ?? false))
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
            .disabled(!(deletingProject.map { canMutate($0.ref.connectionID) } ?? false))
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

    private func connectionSection(_ section: DashboardGroups.Section) -> some View {
        let reachability = appModel.reachability(of: section.connectionID)
        return VStack(alignment: .leading, spacing: 14) {
            HStack(spacing: 9) {
                Circle()
                    .fill(reachability.color)
                    .frame(width: 9, height: 9)
                    .shadow(color: reachability.color.opacity(0.45), radius: 5)

                Text(section.connectionName)
                    .font(.title3.weight(.semibold))

                Text(section.contextLabel == "Local" ? "LOCAL" : "REMOTE")
                    .font(.caption2.monospaced().weight(.semibold))
                    .foregroundStyle(.secondary)
                    .padding(.horizontal, 7)
                    .padding(.vertical, 2)
                    .background(.quaternary, in: RoundedRectangle(cornerRadius: 5))

                if section.contextLabel != "Local" {
                    Text("·  \(section.contextLabel)")
                        .foregroundStyle(.tertiary)
                }

                Text("·  \(projectCountLabel(section.cards.count))")
                    .foregroundStyle(.tertiary)

                Spacer()

                if reachability == .unreachable {
                    Button("Retry", systemImage: "arrow.clockwise") {
                        if let runtime = appModel.runtime(id: section.connectionID) {
                            Task { await runtime.refresh() }
                        }
                    }
                    .labelStyle(.iconOnly)
                    .buttonStyle(.borderless)
                    .help("Retry \(section.connectionName)")
                }
            }

            VStack(spacing: 14) {
                ForEach(section.cards) { card in
                    projectCard(card, reachable: canMutate(section.connectionID))
                }

                if section.cards.isEmpty {
                    Text("No projects")
                        .font(.callout)
                        .foregroundStyle(.tertiary)
                        .frame(maxWidth: .infinity, minHeight: 72, alignment: .center)
                        .overlay {
                            RoundedRectangle(cornerRadius: 12)
                                .stroke(
                                    Color(nsColor: .separatorColor),
                                    style: StrokeStyle(lineWidth: 1, dash: [5, 4])
                                )
                        }
                }
            }
        }
    }

    // MARK: - Project card

    @ViewBuilder
    private func projectCard(_ card: DashboardGroups.ProjectCard, reachable: Bool) -> some View {
        if card.rows.isEmpty && !card.project.isArchived {
            emptyProjectCard(card, reachable: reachable)
        } else {
            VStack(spacing: 0) {
                projectHeader(card, reachable: reachable)

                if !card.rows.isEmpty {
                    Divider()
                    ForEach(Array(card.rows.enumerated()), id: \.element.id) { index, row in
                        workspaceRow(row, reachable: reachable)
                        if index < card.rows.count - 1 {
                            Divider()
                        }
                    }
                }
            }
            .background(Color(nsColor: .controlBackgroundColor), in: cardShape)
            .overlay {
                cardShape.stroke(Color(nsColor: .separatorColor), lineWidth: 1)
            }
            .clipShape(cardShape)
            .opacity(card.project.isArchived ? 0.55 : 1)
        }
    }

    private func projectHeader(
        _ card: DashboardGroups.ProjectCard,
        reachable: Bool
    ) -> some View {
        HStack(spacing: 10) {
            Text(card.project.name)
                .font(.headline)
                .lineLimit(1)

            Text(card.project.workingDir)
                .font(.callout.monospaced())
                .foregroundStyle(.tertiary)
                .lineLimit(1)
                .truncationMode(.head)

            if card.project.isArchived {
                Text("Archived")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                    .padding(.horizontal, 6)
                    .padding(.vertical, 2)
                    .background(.quaternary, in: Capsule())
            }

            Spacer(minLength: 12)

            if !card.project.isArchived {
                Button {
                    onCreateWorkspace(card.ref)
                } label: {
                    Label("New Workspace", systemImage: "plus")
                        .labelStyle(.iconOnly)
                        .frame(width: 22, height: 22)
                }
                .buttonStyle(.borderless)
                .foregroundStyle(.secondary)
                .disabled(!reachable)
                .help("New workspace in \(card.project.name)")
            }
        }
        .padding(.horizontal, 18)
        .frame(minHeight: 54)
        .contentShape(Rectangle())
        .contextMenu { projectMenu(card, reachable: reachable) }
    }

    private func emptyProjectCard(
        _ card: DashboardGroups.ProjectCard,
        reachable: Bool
    ) -> some View {
        HStack(spacing: 10) {
            Text(card.project.name)
                .font(.headline)
                .lineLimit(1)

            Text(card.project.workingDir)
                .font(.callout.monospaced())
                .foregroundStyle(.tertiary)
                .lineLimit(1)
                .truncationMode(.head)

            Text("No workspaces yet")
                .font(.callout)
                .foregroundStyle(.tertiary)
                .italic()

            Spacer(minLength: 12)

            Button("New Workspace", systemImage: "plus") {
                onCreateWorkspace(card.ref)
            }
            .buttonStyle(.bordered)
            .disabled(!reachable)
        }
        .padding(.horizontal, 18)
        .frame(minHeight: 72)
        .contentShape(Rectangle())
        .contextMenu { projectMenu(card, reachable: reachable) }
        .overlay {
            cardShape.stroke(
                Color(nsColor: .separatorColor),
                style: StrokeStyle(lineWidth: 1, dash: [5, 4])
            )
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
            .disabled(!reachable)
            Divider()
            // Mirrors the server rule: archive only once every Workspace
            // is archived. A stale view still gets the 409 via the alert.
            Button("Archive Project", systemImage: "archivebox") {
                if let store = appModel.runtime(id: card.ref.connectionID)?.projects {
                    run { try await store.archive(id: project.id) }
                }
            }
            .disabled(!reachable || card.hasUnarchivedWorkspaces)
        } else {
            Button("Unarchive Project", systemImage: "archivebox") {
                if let store = appModel.runtime(id: card.ref.connectionID)?.projects {
                    run { try await store.unarchive(id: project.id) }
                }
            }
            .disabled(!reachable)
        }
        Divider()
        Button("Delete Project…", systemImage: "trash", role: .destructive) {
            deletingProject = card
        }
        .disabled(!reachable || card.totalWorkspaceCount > 0)
        .help(card.totalWorkspaceCount > 0 ? "Delete all Workspaces first" : "")
    }

    // MARK: - Workspace row

    @ViewBuilder
    private func workspaceRow(
        _ row: DashboardGroups.WorkspaceRow,
        reachable: Bool
    ) -> some View {
        let workspace = row.workspace
        Button {
            onOpenWorkspace(row.ref)
        } label: {
            HStack(spacing: 12) {
                Circle()
                    .fill(row.hasActiveSessions ? Color.green : Color.clear)
                    .frame(width: 8, height: 8)
                    .overlay {
                        if !row.hasActiveSessions {
                            Circle().stroke(.tertiary, lineWidth: 2)
                        }
                    }

                Text(workspace.name)
                    .font(.body.monospaced())
                    .lineLimit(1)

                if workspace.isArchived {
                    Text("Archived")
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                        .padding(.horizontal, 6)
                        .padding(.vertical, 2)
                        .background(.quaternary, in: Capsule())
                }

                Spacer()

                Text(workspaceStatus(row))
                    .font(.callout)
                    .foregroundStyle(.tertiary)
            }
            .padding(.horizontal, 18)
            .frame(minHeight: 48)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .disabled(!reachable)
        .opacity(workspace.isArchived ? 0.5 : 1)
        .contextMenu {
            Button("Open", systemImage: "arrow.up.forward.square") {
                onOpenWorkspace(row.ref)
            }
            .disabled(!reachable)
            Button("Rename…", systemImage: "pencil") {
                renameDraft = workspace.name
                renamingWorkspace = row
            }
            .disabled(!reachable)
            Divider()
            if workspace.isArchived {
                Button("Unarchive", systemImage: "archivebox") {
                    if let store = workspacesStore(for: row.ref.connectionID) {
                        run { try await store.unarchive(id: workspace.id) }
                    }
                }
                .disabled(!reachable)
            } else {
                // Mirrors the server rule: no archiving with active sessions.
                Button("Archive", systemImage: "archivebox") {
                    if let store = workspacesStore(for: row.ref.connectionID) {
                        run { try await store.archive(id: workspace.id) }
                    }
                }
                .disabled(!reachable || row.hasActiveSessions)
            }
            Divider()
            Button("Delete…", systemImage: "trash", role: .destructive) {
                deletingWorkspace = row
            }
            .disabled(!reachable)
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
                .disabled(!appModel.runtimes.contains {
                    appModel.canMutate(connectionID: $0.id)
                })
        }
        .background()
    }

    private var allLoadedOnce: Bool {
        appModel.runtimes.allSatisfy {
            $0.projects.hasLoadedOnce && $0.workspaces.hasLoadedOnce
        }
    }

    // MARK: - Helpers

    private var cardShape: RoundedRectangle {
        RoundedRectangle(cornerRadius: 12, style: .continuous)
    }

    private var canCreateProject: Bool {
        appModel.runtimes.contains { appModel.canMutate(connectionID: $0.id) }
    }

    private func projectCountLabel(_ count: Int) -> String {
        "\(count) \(count == 1 ? "project" : "projects")"
    }

    private func workspaceStatus(_ row: DashboardGroups.WorkspaceRow) -> String {
        if row.hasActiveSessions { return "active now" }
        return row.workspace.updatedAt.formatted(.relative(presentation: .numeric))
    }

    private func canMutate(_ connectionID: UUID) -> Bool {
        appModel.canMutate(connectionID: connectionID)
    }

    private func workspacesStore(for connectionID: UUID) -> WorkspacesStore? {
        appModel.runtime(id: connectionID)?.workspaces
    }

    private func deleteWorkspace(_ row: DashboardGroups.WorkspaceRow) {
        run {
            guard let store = workspacesStore(for: row.ref.connectionID) else { return }
            try await store.delete(id: row.workspace.id)
            WorkspaceSelectionMemory().forget(row.ref)
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

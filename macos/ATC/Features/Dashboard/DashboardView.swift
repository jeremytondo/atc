import AppKit
import SwiftUI
import ATCAPI

/// App-wide Project and Workspace management rendered as a main-content
/// destination inside the stable window split view.
struct DashboardView: View {
    @Environment(AppModel.self) private var appModel
    @Environment(WindowState.self) private var windowState
    /// Opens a Workspace in the shell.
    var onOpenWorkspace: (WorkspaceRef) -> Void
    /// Presents the create-Workspace sheet fixed to a Project.
    var onCreateWorkspace: (ProjectRef) -> Void
    var onCreateProject: () -> Void
    /// Clears window selection memory for a deleted Workspace.
    var onWorkspaceDeleted: (WorkspaceRef) -> Void

    /// Keyboard focus over workspace rows: arrows move, Return opens.
    @State private var focusedWorkspace: WorkspaceRef?
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
            }
        )
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(alignment: .leading, spacing: Spacing.xxl) {
                    ForEach(groups.sections) { section in
                        connectionSection(section)
                    }
                }
                .frame(maxWidth: 1_280, alignment: .leading)
                .padding(.horizontal, Spacing.xxl)
                .padding(.vertical, Spacing.xxl)
                .frame(maxWidth: .infinity, alignment: .top)
            }
            .focusable()
            .focusEffectDisabled()
            .onKeyPress(.downArrow) { moveFocus(1, through: groups.workspaceRefs, proxy: proxy) }
            .onKeyPress(.upArrow) { moveFocus(-1, through: groups.workspaceRefs, proxy: proxy) }
            .onKeyPress(.return) { openFocusedWorkspace() }
        }
        .toolbar {
            ToolbarItemGroup(placement: .primaryAction) {
                Menu {
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
                    appModel.run(on: row.ref.connectionID, reporting: $actionError) {
                        try await store.rename(id: row.workspace.id, name: trimmed)
                    }
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
                    appModel.run(on: card.ref.connectionID, reporting: $actionError) {
                        try await store.rename(id: card.project.id, name: trimmed)
                    }
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
                if let card = deletingProject {
                    appModel.run(on: card.ref.connectionID, reporting: $actionError) {
                        try await appModel.deleteProject(card.ref)
                    }
                }
            }
            .disabled(!(deletingProject.map { canMutate($0.ref.connectionID) } ?? false))
        } message: {
            if let card = deletingProject {
                Text(DeleteConfirmation.projectMessage(name: card.project.name))
            }
        }
        .actionErrorAlert($actionError)
    }

    // MARK: - Connection section

    private func connectionSection(_ section: DashboardGroups.Section) -> some View {
        let reachability = appModel.reachability(of: section.connectionID)
        return VStack(alignment: .leading, spacing: Spacing.md) {
            HStack(spacing: Spacing.sm) {
                StatusDot(color: reachability.color)

                Text(section.connectionName)
                    .font(.title3.weight(.semibold))

                TagBadge(
                    text: section.contextLabel == "Local" ? "LOCAL" : "REMOTE",
                    monospaced: true
                )

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

            VStack(spacing: Spacing.md) {
                ForEach(section.cards) { card in
                    projectCard(
                        card,
                        reachable: canMutate(section.connectionID),
                        // Opening is local navigation over cached data; only
                        // a confirmed-unreachable Connection blocks it.
                        canOpen: reachability != .unreachable
                    )
                }

                if section.cards.isEmpty {
                    Text("No projects")
                        .font(.callout)
                        .foregroundStyle(.tertiary)
                        .frame(maxWidth: .infinity, minHeight: 72, alignment: .center)
                        .overlay {
                            RoundedRectangle(cornerRadius: Radius.card)
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
    private func projectCard(
        _ card: DashboardGroups.ProjectCard,
        reachable: Bool,
        canOpen: Bool
    ) -> some View {
        if card.rows.isEmpty {
            emptyProjectCard(card, reachable: reachable)
        } else {
            VStack(spacing: 0) {
                projectHeader(card, reachable: reachable)

                if !card.rows.isEmpty {
                    Divider()
                    ForEach(Array(card.rows.enumerated()), id: \.element.id) { index, row in
                        workspaceRow(row, reachable: reachable, canOpen: canOpen)
                            .id(row.ref)
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
        }
    }

    private func projectHeader(
        _ card: DashboardGroups.ProjectCard,
        reachable: Bool
    ) -> some View {
        HStack(spacing: Spacing.sm) {
            Text(card.project.name)
                .font(.headline)
                .lineLimit(1)

            Text(card.project.workingDir)
                .font(.callout.monospaced())
                .foregroundStyle(.tertiary)
                .lineLimit(1)
                .truncationMode(.head)

            Spacer(minLength: Spacing.md)

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
        .padding(.horizontal, Spacing.lg)
        .frame(minHeight: 54)
        .contentShape(Rectangle())
        .contextMenu { projectMenu(card, reachable: reachable) }
    }

    private func emptyProjectCard(
        _ card: DashboardGroups.ProjectCard,
        reachable: Bool
    ) -> some View {
        HStack(spacing: Spacing.sm) {
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

            Spacer(minLength: Spacing.md)

            Button("New Workspace", systemImage: "plus") {
                onCreateWorkspace(card.ref)
            }
            .buttonStyle(.bordered)
            .disabled(!reachable)
        }
        .padding(.horizontal, Spacing.lg)
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
        Button("New Workspace…", systemImage: "plus") {
            onCreateWorkspace(card.ref)
        }
        .disabled(!reachable)
        Button("Rename…", systemImage: "pencil") {
            renameDraft = project.name
            renamingProject = card
        }
        .disabled(!reachable)
        Button("Workspace Startup…", systemImage: "rectangle.stack.badge.plus") {
            windowState.workspaceStartupProject = card.ref
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
        reachable: Bool,
        canOpen: Bool
    ) -> some View {
        let workspace = row.workspace
        Button {
            onOpenWorkspace(row.ref)
        } label: {
            HStack(spacing: Spacing.md) {
                StatusDot(color: .green, hollow: !row.hasActiveSessions)

                Text(workspace.name)
                    .font(.body.monospaced())
                    .lineLimit(1)

                Spacer()

                Text(workspaceStatus(row))
                    .font(.callout)
                    .foregroundStyle(.tertiary)
            }
            .padding(.horizontal, Spacing.lg)
            .frame(minHeight: 48)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .background(
            focusedWorkspace == row.ref
                ? Color(nsColor: .quaternarySystemFill)
                : .clear
        )
        .disabled(!canOpen)
        .contextMenu {
            Button("Open", systemImage: "arrow.up.forward.square") {
                onOpenWorkspace(row.ref)
            }
            .disabled(!canOpen)
            Button("Rename…", systemImage: "pencil") {
                renameDraft = workspace.name
                renamingWorkspace = row
            }
            .disabled(!reachable)
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
        .background(AppColors.canvas)
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
        .background(AppColors.canvas)
    }

    private var allLoadedOnce: Bool {
        appModel.runtimes.allSatisfy {
            $0.projects.hasLoadedOnce && $0.workspaces.hasLoadedOnce
        }
    }

    // MARK: - Helpers

    private var cardShape: RoundedRectangle {
        RoundedRectangle(cornerRadius: Radius.card, style: .continuous)
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

    // MARK: - Keyboard navigation

    private func moveFocus(
        _ delta: Int,
        through refs: [WorkspaceRef],
        proxy: ScrollViewProxy
    ) -> KeyPress.Result {
        guard !refs.isEmpty else { return .ignored }
        let current = focusedWorkspace.flatMap { refs.firstIndex(of: $0) }
        let next = current.map { max(0, min(refs.count - 1, $0 + delta)) }
            ?? (delta > 0 ? 0 : refs.count - 1)
        focusedWorkspace = refs[next]
        proxy.scrollTo(refs[next])
        return .handled
    }

    private func openFocusedWorkspace() -> KeyPress.Result {
        guard let focusedWorkspace else { return .ignored }
        onOpenWorkspace(focusedWorkspace)
        return .handled
    }

    private func workspacesStore(for connectionID: UUID) -> WorkspacesStore? {
        appModel.runtime(id: connectionID)?.workspaces
    }

    private func deleteWorkspace(_ row: DashboardGroups.WorkspaceRow) {
        guard let store = workspacesStore(for: row.ref.connectionID) else { return }
        appModel.run(on: row.ref.connectionID, reporting: $actionError) {
            try await store.delete(id: row.workspace.id)
            onWorkspaceDeleted(row.ref)
        }
    }
}

#Preview {
    DashboardView(
        onOpenWorkspace: { _ in },
        onCreateWorkspace: { _ in },
        onCreateProject: {},
        onWorkspaceDeleted: { _ in }
    )
        .environment(AppModel.preview())
        .environment(WindowState())
        .preferredColorScheme(.dark)
}

import SwiftUI
import ATCAPI

struct NavigatorSidebar: View {
    @Environment(WindowState.self) private var windowState

    var body: some View {
        @Bindable var windowState = windowState
        VStack(spacing: 0) {
            NavigatorSelector(selection: $windowState.selectedNavigator)
            switch windowState.selectedNavigator {
            case .projects:
                ProjectsNavigatorView()
            case .workspace:
                WorkspaceNavigatorView()
            case .file:
                FileNavigatorView()
            }
        }
        .navigationSplitViewColumnWidth(min: 220, ideal: 280)
    }
}

/// Native segmented navigation in an inset system surface, matching the
/// hierarchy and interaction model of Xcode's Navigator selector.
struct NavigatorSelector: View {
    @Environment(WindowState.self) private var windowState
    @Binding var selection: NavigatorID

    var body: some View {
        let options = NavigatorSelectorOption.all(
            hasActiveWorkspace: windowState.activeWorkspace != nil
        )
        Picker("Navigator", selection: $selection) {
            ForEach(options) { option in
                Image(systemName: option.id.systemImage)
                    .accessibilityLabel(option.id.label)
                    .help(option.help)
                    .tag(option.id)
                    // .disabled is inert on segmented picker options;
                    // only .selectionDisabled actually blocks selection.
                    .selectionDisabled(!option.isEnabled)
            }
        }
        .pickerStyle(.segmented)
        .labelsHidden()
        .controlSize(.regular)
        .tint(.accentColor)
        .padding(2)
        .background(.quaternary, in: RoundedRectangle(
            cornerRadius: Radius.control,
            style: .continuous
        ))
        .padding(.horizontal, Spacing.md)
        .padding(.bottom, NavigatorMetrics.selectorToContentSpacing)
    }
}

struct NavigatorSelectorOption: Identifiable, Equatable {
    let id: NavigatorID
    let isEnabled: Bool
    let help: String

    static func all(hasActiveWorkspace: Bool) -> [NavigatorSelectorOption] {
        NavigatorID.allCases.map { navigator in
            let enabled = navigator == .projects || hasActiveWorkspace
            return NavigatorSelectorOption(
                id: navigator,
                isEnabled: enabled,
                help: enabled ? navigator.label : "Requires an Active Workspace"
            )
        }
    }
}

struct ProjectsNavigatorView: View {
    @Environment(AppModel.self) private var appModel
    @Environment(WindowState.self) private var windowState

    @State private var renamingProject: ProjectsNavigatorGroups.ProjectGroup?
    @State private var renamingWorkspace: ProjectsNavigatorGroups.WorkspaceRow?
    @State private var renameDraft = ""
    @State private var deletingProject: ProjectsNavigatorGroups.ProjectGroup?
    @State private var deletingWorkspace: ProjectsNavigatorGroups.WorkspaceRow?
    @State private var actionError: String?

    var body: some View {
        @Bindable var windowState = windowState
        let groups = ProjectsNavigatorGroups(inputs: appModel.runtimes.map {
            .init(
                connection: $0.record,
                reachability: $0.reachability,
                projects: $0.projects.projects,
                workspaces: $0.workspaces.workspaces,
                sessions: $0.sessions.sessions
            )
        })
        List {
            NavigatorRow(
                isSelected: windowState.selectedContent == .dashboard,
                action: { windowState.showDashboard() }
            ) { _ in
                NavigatorIconLabel(title: "Dashboard", systemImage: "rectangle.3.group")
            } actions: {
                EmptyView()
            }

            NavigatorDisclosureHeader(
                title: "Projects",
                isExpanded: $windowState.isProjectsSectionExpanded,
                addHelp: "New project",
                isAddEnabled: true,
                onAdd: { windowState.isCreateProjectPresented = true }
            )

            if windowState.isProjectsSectionExpanded {
                ForEach(groups.projects) { group in
                    projectRow(group)
                    ForEach(group.workspaces) { row in
                        if windowState.expandedProjects.contains(group.ref) {
                            workspaceRow(row, in: group)
                        }
                    }
                    if windowState.expandedProjects.contains(group.ref), group.workspaces.isEmpty {
                        Text("No workspaces")
                            .font(.caption)
                            .foregroundStyle(.tertiary)
                            .padding(.leading, NavigatorMetrics.nestedIndent)
                            .navigatorListRow()
                    }
                }
            }
        }
        .navigatorList()
        .alert("Rename Project", isPresented: Binding(
            get: { renamingProject != nil },
            set: { if !$0 { renamingProject = nil } }
        )) {
            TextField("Name", text: $renameDraft)
            Button("Rename") {
                if let group = renamingProject,
                   let store = appModel.runtime(id: group.ref.connectionID)?.projects {
                    let name = renameDraft.trimmingCharacters(in: .whitespaces)
                    run(on: group.ref.connectionID) {
                        try await store.rename(id: group.project.id, name: name)
                    }
                }
            }
            .disabled(
                renameDraft.trimmingCharacters(in: .whitespaces).isEmpty
                    || !(renamingProject.map { canMutate($0.ref.connectionID) } ?? false)
            )
            Button("Cancel", role: .cancel) {}
        }
        .alert("Rename Workspace", isPresented: Binding(
            get: { renamingWorkspace != nil },
            set: { if !$0 { renamingWorkspace = nil } }
        )) {
            TextField("Name", text: $renameDraft)
            Button("Rename") {
                if let row = renamingWorkspace,
                   let store = appModel.runtime(id: row.ref.connectionID)?.workspaces {
                    let name = renameDraft.trimmingCharacters(in: .whitespaces)
                    run(on: row.ref.connectionID) {
                        try await store.rename(id: row.workspace.id, name: name)
                    }
                }
            }
            .disabled(
                renameDraft.trimmingCharacters(in: .whitespaces).isEmpty
                    || !(renamingWorkspace.map { canMutate($0.ref.connectionID) } ?? false)
            )
            Button("Cancel", role: .cancel) {}
        }
        .confirmationDialog(
            "Delete Project “\(deletingProject?.project.name ?? "")”?",
            isPresented: Binding(
                get: { deletingProject != nil },
                set: { if !$0 { deletingProject = nil } }
            )
        ) {
            Button("Delete Project", role: .destructive) {
                if let group = deletingProject,
                   let store = appModel.runtime(id: group.ref.connectionID)?.projects {
                    run(on: group.ref.connectionID) {
                        try await store.delete(id: group.project.id)
                    }
                }
            }
            .disabled(!(deletingProject.map { canMutate($0.ref.connectionID) } ?? false))
        }
        .confirmationDialog(
            "Delete Workspace “\(deletingWorkspace?.workspace.name ?? "")”?",
            isPresented: Binding(
                get: { deletingWorkspace != nil },
                set: { if !$0 { deletingWorkspace = nil } }
            )
        ) {
            Button("Delete Workspace", role: .destructive) {
                if let row = deletingWorkspace,
                   let store = appModel.runtime(id: row.ref.connectionID)?.workspaces {
                    run(on: row.ref.connectionID) {
                        try await store.delete(id: row.workspace.id)
                        windowState.forgetSelection(for: row.ref)
                    }
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
        .actionErrorAlert($actionError)
    }

    private func projectRow(_ group: ProjectsNavigatorGroups.ProjectGroup) -> some View {
        NavigatorRow(
            action: {
                if windowState.expandedProjects.contains(group.ref) {
                    windowState.expandedProjects.remove(group.ref)
                } else {
                    windowState.expandedProjects.insert(group.ref)
                }
            }
        ) { isHovering in
            HStack(spacing: Spacing.sm) {
                Image(systemName: windowState.expandedProjects.contains(group.ref) ? "folder.fill" : "folder")
                    .frame(width: NavigatorMetrics.iconWidth, alignment: .leading)
                    .foregroundStyle(.secondary)
                Text(group.project.name)
                    .lineLimit(1)
                Spacer(minLength: Spacing.sm)
                if !isHovering {
                    HStack(spacing: Spacing.xs) {
                        Text(group.connectionName)
                            .lineLimit(1)
                        StatusDot(color: group.reachability.color, size: .inline)
                    }
                    .font(.caption)
                    .foregroundStyle(.secondary)
                }
            }
        } actions: {
            NavigatorActionMenu(systemImage: "ellipsis", help: "Project actions") {
                projectMenu(group)
            }
            NavigatorActionButton(
                systemImage: "plus",
                help: "New workspace",
                isEnabled: group.reachability == .connected
            ) {
                windowState.createWorkspaceContext = .init(mode: .fixed(group.ref))
            }
        }
        .contextMenu { projectMenu(group) }
    }

    @ViewBuilder
    private func projectMenu(_ group: ProjectsNavigatorGroups.ProjectGroup) -> some View {
        Button("New Workspace…", systemImage: "plus") {
            windowState.createWorkspaceContext = .init(mode: .fixed(group.ref))
        }
        .disabled(group.reachability != .connected)
        Button("Rename…", systemImage: "pencil") {
            renameDraft = group.project.name
            renamingProject = group
        }
        .disabled(group.reachability != .connected)
        Divider()
        Button("Archive Project", systemImage: "archivebox") {
            if let store = appModel.runtime(id: group.ref.connectionID)?.projects {
                run(on: group.ref.connectionID) {
                    try await store.archive(id: group.project.id)
                }
            }
        }
        .disabled(group.reachability != .connected || group.hasUnarchivedWorkspaces)
        Divider()
        Button("Delete Project…", systemImage: "trash", role: .destructive) {
            deletingProject = group
        }
        .disabled(group.reachability != .connected || group.totalWorkspaceCount > 0)
    }

    private func workspaceRow(
        _ row: ProjectsNavigatorGroups.WorkspaceRow,
        in group: ProjectsNavigatorGroups.ProjectGroup
    ) -> some View {
        let enabled = group.reachability == .connected
        let selected = windowState.selectedContent != .dashboard
            && windowState.activeWorkspace == row.ref
        return NavigatorRow(
            isSelected: selected,
            isEnabled: enabled,
            leadingIndent: NavigatorMetrics.nestedIndent,
            action: {
                _ = windowState.activateWorkspace(row.ref, in: appModel)
            }
        ) { _ in
            Text(row.workspace.name)
                .lineLimit(1)
        } actions: {
            NavigatorActionButton(
                systemImage: "archivebox",
                help: row.hasActiveSessions ? "Stop active sessions before archiving" : "Archive workspace",
                isEnabled: !row.hasActiveSessions
            ) {
                archiveWorkspace(row)
            }
        }
        .opacity(enabled ? 1 : Dimming.archived)
        .contextMenu { workspaceMenu(row, enabled: enabled) }
    }

    @ViewBuilder
    private func workspaceMenu(
        _ row: ProjectsNavigatorGroups.WorkspaceRow,
        enabled: Bool
    ) -> some View {
        Button("Open", systemImage: "arrow.up.forward.square") {
            _ = windowState.activateWorkspace(row.ref, in: appModel)
        }
        .disabled(!enabled)
        Button("New Session", systemImage: "plus.bubble") {
            presentStart(.agentSession, in: row.ref)
        }
        .disabled(!enabled)
        Button("New Terminal", systemImage: "terminal") {
            presentStart(.terminal, in: row.ref)
        }
        .disabled(!enabled)
        Divider()
        Button("Rename…", systemImage: "pencil") {
            renameDraft = row.workspace.name
            renamingWorkspace = row
        }
        .disabled(!enabled)
        Button("Archive", systemImage: "archivebox") {
            archiveWorkspace(row)
        }
        .disabled(!enabled || row.hasActiveSessions)
        Divider()
        Button("Delete…", systemImage: "trash", role: .destructive) {
            deletingWorkspace = row
        }
        .disabled(!enabled)
    }

    private func archiveWorkspace(_ row: ProjectsNavigatorGroups.WorkspaceRow) {
        guard let store = appModel.runtime(id: row.ref.connectionID)?.workspaces else { return }
        run(on: row.ref.connectionID) {
            try await store.archive(id: row.workspace.id)
        }
    }

    private func presentStart(_ kind: StartSessionKind, in ref: WorkspaceRef) {
        guard windowState.activateWorkspace(ref, in: appModel),
              appModel.canStartSession(in: ref)
        else { return }
        windowState.startSessionKind = kind
    }

    private func canMutate(_ connectionID: UUID) -> Bool {
        appModel.canMutate(connectionID: connectionID)
    }

    private func run(
        on connectionID: UUID,
        _ operation: @escaping () async throws -> Void
    ) {
        Task {
            guard canMutate(connectionID) else {
                actionError = "The connection is unavailable."
                return
            }
            do { try await operation() }
            catch { actionError = error.localizedDescription }
        }
    }
}

struct FileNavigatorView: View {
    static let unavailableMessage = "File navigation is not available yet"

    var body: some View {
        ContentUnavailableView(
            Self.unavailableMessage,
            systemImage: "doc"
        )
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}

import SwiftUI
import ATCAPI

struct NavigatorSidebar: View {
    @Environment(WindowState.self) private var windowState

    var body: some View {
        @Bindable var windowState = windowState
        VStack(spacing: 0) {
            NavigatorSelector(selection: $windowState.selectedNavigator)
            Divider()
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

struct NavigatorSelector: View {
    @Environment(WindowState.self) private var windowState
    @Binding var selection: NavigatorID

    var body: some View {
        HStack(spacing: 6) {
            ForEach(NavigatorSelectorOption.all(
                hasActiveWorkspace: windowState.activeWorkspace != nil
            )) { option in
                Button {
                    selection = option.id
                } label: {
                    Image(systemName: option.id.systemImage)
                        .font(.system(size: 13, weight: .medium))
                        .foregroundStyle(
                            selection == option.id ? Color.white : Color.secondary
                        )
                        .frame(width: 30, height: 26)
                        .background(
                            selection == option.id ? Color.accentColor : .clear,
                            in: RoundedRectangle(cornerRadius: 6, style: .continuous)
                        )
                }
                .buttonStyle(.plain)
                .disabled(!option.isEnabled)
                .help(option.help)
                .accessibilityLabel(option.id.label)
            }
            Spacer()
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 6)
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
        let groups = ProjectsNavigatorGroups(inputs: appModel.runtimes.map {
            .init(
                connection: $0.record,
                reachability: $0.reachability,
                projects: $0.projects.projects,
                workspaces: $0.workspaces.workspaces,
                sessions: $0.sessions.sessions
            )
        })
        List(selection: selectionBinding) {
            Label("Dashboard", systemImage: "rectangle.3.group")
                .tag(ProjectsNavigatorSelection.dashboard)

            ForEach(groups.projects) { group in
                DisclosureGroup(isExpanded: expansionBinding(for: group.ref)) {
                    ForEach(group.workspaces) { row in
                        workspaceRow(row, in: group)
                    }
                    if group.workspaces.isEmpty {
                        Text("No workspaces")
                            .font(.caption)
                            .foregroundStyle(.tertiary)
                            .padding(.leading, Spacing.sm)
                    }
                } label: {
                    projectRow(group)
                }
            }
        }
        .listStyle(.sidebar)
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
        .alert("Action Failed", isPresented: Binding(
            get: { actionError != nil },
            set: { if !$0 { actionError = nil } }
        )) {
            Button("OK", role: .cancel) {}
        } message: {
            Text(actionError ?? "")
        }
    }

    private func projectRow(_ group: ProjectsNavigatorGroups.ProjectGroup) -> some View {
        HStack(spacing: Spacing.sm) {
            Image(systemName: "folder")
            VStack(alignment: .leading, spacing: 1) {
                Text(group.project.name).lineLimit(1)
                HStack(spacing: Spacing.xs) {
                    StatusDot(color: group.reachability.color, size: .inline)
                    Text(group.connectionName)
                }
                .font(.caption2)
                .foregroundStyle(.secondary)
            }
            Spacer()
        }
        .contentShape(Rectangle())
        .onTapGesture { windowState.focusedProject = group.ref }
        .contextMenu {
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
    }

    private func workspaceRow(
        _ row: ProjectsNavigatorGroups.WorkspaceRow,
        in group: ProjectsNavigatorGroups.ProjectGroup
    ) -> some View {
        let enabled = group.reachability == .connected
        return HStack(spacing: Spacing.sm) {
            Image(systemName: "square.on.square")
                .font(.caption)
                .foregroundStyle(.secondary)
            Text(row.workspace.name).lineLimit(1)
            Spacer()
        }
        .contentShape(Rectangle())
        .tag(ProjectsNavigatorSelection.workspace(row.ref))
        .disabled(!enabled)
        .opacity(enabled ? 1 : Dimming.archived)
        .contextMenu {
            Button("Open", systemImage: "arrow.up.forward.square") {
                _ = windowState.activateWorkspace(row.ref, in: appModel)
            }
            .disabled(!enabled)
            Button("Rename…", systemImage: "pencil") {
                renameDraft = row.workspace.name
                renamingWorkspace = row
            }
            .disabled(!enabled)
            Divider()
            Button("Archive", systemImage: "archivebox") {
                if let store = appModel.runtime(id: row.ref.connectionID)?.workspaces {
                    run(on: row.ref.connectionID) {
                        try await store.archive(id: row.workspace.id)
                    }
                }
            }
            .disabled(!enabled || row.hasActiveSessions)
            Divider()
            Button("Delete…", systemImage: "trash", role: .destructive) {
                deletingWorkspace = row
            }
            .disabled(!enabled)
        }
    }

    private func expansionBinding(for ref: ProjectRef) -> Binding<Bool> {
        Binding(
            get: { windowState.expandedProjects.contains(ref) },
            set: { expanded in
                if expanded { windowState.expandedProjects.insert(ref) }
                else { windowState.expandedProjects.remove(ref) }
            }
        )
    }

    private var selectionBinding: Binding<ProjectsNavigatorSelection?> {
        Binding(
            get: {
                if windowState.selectedContent == .dashboard { return .dashboard }
                return windowState.activeWorkspace.map(ProjectsNavigatorSelection.workspace)
            },
            set: { selection in
                switch selection {
                case .dashboard:
                    if windowState.selectedContent != .dashboard {
                        windowState.showDashboard()
                    }
                case .workspace(let ref):
                    if windowState.activeWorkspace != ref
                        || windowState.selectedContent == .dashboard {
                        windowState.focusedWorkspace = ref
                        _ = windowState.activateWorkspace(ref, in: appModel)
                    }
                case nil:
                    break
                }
            }
        )
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

private enum ProjectsNavigatorSelection: Hashable {
    case dashboard
    case workspace(WorkspaceRef)
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

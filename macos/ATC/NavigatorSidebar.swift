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
        HStack(spacing: 4) {
            ForEach(NavigatorID.allCases, id: \.self) { navigator in
                let enabled = navigator == .projects || windowState.activeWorkspace != nil
                Button {
                    selection = navigator
                } label: {
                    Image(systemName: navigator.systemImage)
                        .frame(width: 28, height: 24)
                        .background(
                            selection == navigator ? Color.accentColor.opacity(0.18) : .clear,
                            in: RoundedRectangle(cornerRadius: 5)
                        )
                }
                .buttonStyle(.plain)
                .disabled(!enabled)
                .help(enabled ? navigator.label : "Requires an Active Workspace")
                .accessibilityLabel(navigator.label)
            }
            Spacer()
        }
        .padding(.horizontal, 10)
        .padding(.vertical, 7)
        .background(.bar)
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
        List {
            Button {
                windowState.showDashboard()
            } label: {
                Label("Dashboard", systemImage: "square.grid.2x2")
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .listRowBackground(
                windowState.selectedContent == .dashboard
                    ? Color.accentColor.opacity(0.16) : Color.clear
            )

            ForEach(groups.projects) { group in
                DisclosureGroup(isExpanded: expansionBinding(for: group.ref)) {
                    ForEach(group.workspaces) { row in
                        workspaceRow(row, in: group)
                    }
                    if group.workspaces.isEmpty {
                        Text("No workspaces")
                            .font(.caption)
                            .foregroundStyle(.tertiary)
                            .padding(.leading, 8)
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
                    run { try await store.rename(id: group.project.id, name: name) }
                }
            }
            .disabled(renameDraft.trimmingCharacters(in: .whitespaces).isEmpty)
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
                    run { try await store.rename(id: row.workspace.id, name: name) }
                }
            }
            .disabled(renameDraft.trimmingCharacters(in: .whitespaces).isEmpty)
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
                    run { try await store.delete(id: group.project.id) }
                }
            }
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
                    run {
                        try await store.delete(id: row.workspace.id)
                        windowState.forgetSelection(for: row.ref)
                    }
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
        HStack(spacing: 6) {
            Image(systemName: "folder")
            VStack(alignment: .leading, spacing: 1) {
                Text(group.project.name).lineLimit(1)
                HStack(spacing: 4) {
                    Circle()
                        .fill(group.reachability.color)
                        .frame(width: 6, height: 6)
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
                    run { try await store.archive(id: group.project.id) }
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
        return Button {
            windowState.focusedWorkspace = row.ref
            _ = windowState.activateWorkspace(row.ref, in: appModel)
        } label: {
            HStack {
                Image(systemName: "square.on.square")
                    .foregroundStyle(.secondary)
                Text(row.workspace.name).lineLimit(1)
                Spacer()
            }
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .disabled(!enabled)
        .opacity(enabled ? 1 : 0.55)
        .listRowBackground(
            windowState.activeWorkspace == row.ref
                ? Color.accentColor.opacity(0.16) : Color.clear
        )
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
                    run { try await store.archive(id: row.workspace.id) }
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

    private func run(_ operation: @escaping () async throws -> Void) {
        Task {
            do { try await operation() }
            catch { actionError = error.localizedDescription }
        }
    }
}

struct FileNavigatorView: View {
    var body: some View {
        ContentUnavailableView(
            "File navigation is not available yet",
            systemImage: "doc"
        )
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}

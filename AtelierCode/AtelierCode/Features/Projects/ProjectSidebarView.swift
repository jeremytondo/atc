import SwiftUI
import CockpitAPI

/// Sidebar: projects as collapsible groups with their sessions nested
/// underneath. Selection is always a session ID — project rows are
/// disclosure headers that carry the per-project actions (new session,
/// rename, archive). Sessions that predate projects land in a trailing
/// "Other Sessions" group so nothing disappears during migration.
struct ProjectSidebarView: View {
    @Environment(AppModel.self) private var appModel
    @Binding var selection: String?
    let searchText: String
    let connectedIDs: Set<String>
    /// The shell presents the New Session sheet for this project.
    @Binding var newSessionProject: Project?
    /// The shell presents the New Project sheet.
    var onCreateProject: () -> Void = {}

    /// Groups start expanded; only explicit collapses are remembered.
    @State private var collapsedProjectIDs: Set<String> = []
    @State private var renamingProject: Project?
    @State private var renameDraft = ""
    @State private var actionError: String?

    var body: some View {
        List(selection: $selection) {
            ForEach(visibleProjects) { project in
                DisclosureGroup(isExpanded: expansionBinding(project.id)) {
                    let group = sessions(in: project)
                    ForEach(group) { session in
                        SessionRowView(
                            session: session,
                            isConnected: connectedIDs.contains(session.id),
                            showsWorkingDir: false
                        )
                        .tag(session.id)
                    }
                    if group.isEmpty {
                        Text("No sessions")
                            .font(.caption)
                            .foregroundStyle(.tertiary)
                    }
                } label: {
                    projectLabel(project)
                }
            }
            let unscoped = unscopedSessions
            if !unscoped.isEmpty {
                Section("Other Sessions") {
                    ForEach(unscoped) { session in
                        SessionRowView(session: session, isConnected: connectedIDs.contains(session.id))
                            .tag(session.id)
                    }
                }
            }
        }
        .overlay {
            if visibleProjects.isEmpty && unscopedSessions.isEmpty
                && appModel.projects.hasLoadedOnce && appModel.sessions.hasLoadedOnce {
                emptyState
            }
        }
        .alert("Rename Project", isPresented: Binding(
            get: { renamingProject != nil },
            set: { if !$0 { renamingProject = nil } }
        )) {
            TextField("Name", text: $renameDraft)
            Button("Rename") {
                if let project = renamingProject {
                    run { try await appModel.projects.rename(id: project.id, name: renameDraft) }
                }
            }
            .disabled(renameDraft.trimmingCharacters(in: .whitespaces).isEmpty)
            Button("Cancel", role: .cancel) {}
        }
        .alert("Project Action Failed", isPresented: Binding(
            get: { actionError != nil },
            set: { if !$0 { actionError = nil } }
        )) {
            Button("OK", role: .cancel) {}
        } message: {
            Text(actionError ?? "")
        }
    }

    // MARK: - Project row

    @ViewBuilder
    private func projectLabel(_ project: Project) -> some View {
        HStack {
            Label {
                Text(project.name)
                    .lineLimit(1)
            } icon: {
                Image(systemName: project.isArchived ? "archivebox" : "folder")
            }
            .foregroundStyle(project.isArchived ? AnyShapeStyle(.secondary) : AnyShapeStyle(.primary))
            Spacer()
            if !project.isArchived {
                Button {
                    newSessionProject = project
                } label: {
                    Image(systemName: "plus")
                }
                .buttonStyle(.plain)
                .foregroundStyle(.secondary)
                .help("New session in \(project.name)")
            }
        }
        .contextMenu {
            if !project.isArchived {
                Button("New Session", systemImage: "plus") {
                    newSessionProject = project
                }
                Button("Rename…", systemImage: "pencil") {
                    renameDraft = project.name
                    renamingProject = project
                }
                Divider()
                Button("Archive Project", systemImage: "archivebox") {
                    run { try await appModel.projects.archive(id: project.id) }
                }
            } else {
                Button("Unarchive Project", systemImage: "archivebox") {
                    run { try await appModel.projects.unarchive(id: project.id) }
                }
            }
        }
        .help(project.workingDir)
    }

    private var emptyState: some View {
        Group {
            if let error = appModel.projects.lastError ?? appModel.sessions.lastError {
                ContentUnavailableView {
                    Label("Can't Reach Cockpit", systemImage: "wifi.exclamationmark")
                } description: {
                    Text(error)
                } actions: {
                    Button("Retry") {
                        Task {
                            await appModel.projects.refresh()
                            await appModel.sessions.refresh()
                        }
                    }
                }
            } else if searchText.isEmpty {
                ContentUnavailableView {
                    Label("No Projects", systemImage: "folder")
                } description: {
                    Text("Create a project to start working in a codebase.")
                } actions: {
                    Button("New Project") { onCreateProject() }
                }
            } else {
                ContentUnavailableView(
                    "No Matches",
                    systemImage: "magnifyingglass",
                    description: Text("Nothing matches “\(searchText)”.")
                )
            }
        }
    }

    // MARK: - Grouping & filtering

    private var visibleProjects: [Project] {
        let all = appModel.projects.projects
        guard !searchText.isEmpty else { return all }
        // A project stays visible if its own name matches or any of its
        // sessions do.
        return all.filter { project in
            project.name.localizedCaseInsensitiveContains(searchText)
                || project.workingDir.localizedCaseInsensitiveContains(searchText)
                || !sessions(in: project).isEmpty
        }
    }

    /// Sessions for one project, newest activity first. When searching, a
    /// matching project shows all its sessions; otherwise only matching
    /// sessions survive.
    private func sessions(in project: Project) -> [Session] {
        let members = appModel.sessions.sessions
            .filter { $0.project?.id == project.id }
            .sorted { $0.updatedAt > $1.updatedAt }
        guard !searchText.isEmpty else { return members }
        if project.name.localizedCaseInsensitiveContains(searchText)
            || project.workingDir.localizedCaseInsensitiveContains(searchText) {
            return members
        }
        return members.filter(matches)
    }

    private var unscopedSessions: [Session] {
        let unscoped = appModel.sessions.sessions
            .filter { $0.project == nil }
            .sorted { $0.updatedAt > $1.updatedAt }
        guard !searchText.isEmpty else { return unscoped }
        return unscoped.filter(matches)
    }

    private func matches(_ session: Session) -> Bool {
        session.displayName.localizedCaseInsensitiveContains(searchText)
            || session.workingDir.localizedCaseInsensitiveContains(searchText)
            || session.action.localizedCaseInsensitiveContains(searchText)
    }

    private func expansionBinding(_ projectID: String) -> Binding<Bool> {
        Binding(
            get: { !collapsedProjectIDs.contains(projectID) },
            set: { expanded in
                if expanded {
                    collapsedProjectIDs.remove(projectID)
                } else {
                    collapsedProjectIDs.insert(projectID)
                }
            }
        )
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

import SwiftUI
import CockpitAPI

/// Sidebar: projects as collapsible groups with their sessions nested
/// underneath. Selection is always a session ID — project rows are
/// disclosure headers that carry the per-project actions (new session,
/// rename, archive). Sessions whose project is hidden (archived) or gone,
/// and legacy unscoped sessions, land in a trailing "Other Sessions" group
/// so nothing becomes unreachable.
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
        // One pass over the stores per render; the List body indexes into it.
        let groups = SidebarGroups(
            projects: appModel.projects.projects,
            sessions: appModel.sessions.sessions,
            searchText: searchText
        )
        List(selection: $selection) {
            ForEach(groups.visibleProjects) { project in
                DisclosureGroup(isExpanded: expansionBinding(project.id)) {
                    let members = groups.sessions(in: project.id)
                    ForEach(members) { session in
                        SessionRowView(
                            session: session,
                            isConnected: connectedIDs.contains(session.id),
                            showsWorkingDir: false
                        )
                        .tag(session.id)
                    }
                    if members.isEmpty {
                        Text("No sessions")
                            .font(.caption)
                            .foregroundStyle(.tertiary)
                    }
                } label: {
                    projectLabel(project)
                }
            }
            if !groups.otherSessions.isEmpty {
                Section("Other Sessions") {
                    ForEach(groups.otherSessions) { session in
                        SessionRowView(session: session, isConnected: connectedIDs.contains(session.id))
                            .tag(session.id)
                    }
                }
            }
        }
        .overlay {
            if groups.isEmpty
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
                    let trimmed = renameDraft.trimmingCharacters(in: .whitespaces)
                    run { try await appModel.projects.rename(id: project.id, name: trimmed) }
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

/// The sidebar's grouping/filtering, computed once per render. Search rules:
/// a project whose own name or directory matches shows all its sessions;
/// otherwise it stays visible only if some of its sessions match, filtered
/// down to those. Within a group, active sessions come first (newest
/// activity), archived ones trail.
struct SidebarGroups {
    let visibleProjects: [Project]
    let otherSessions: [Session]
    private let sessionsByProject: [String: [Session]]

    var isEmpty: Bool { visibleProjects.isEmpty && otherSessions.isEmpty }

    func sessions(in projectID: String) -> [Session] {
        sessionsByProject[projectID] ?? []
    }

    init(projects: [Project], sessions: [Session], searchText: String) {
        let byProject = Dictionary(grouping: sessions, by: { $0.project?.id })
        let listedProjectIDs = Set(projects.map(\.id))

        func matches(_ session: Session) -> Bool {
            session.displayName.localizedCaseInsensitiveContains(searchText)
                || session.workingDir.localizedCaseInsensitiveContains(searchText)
                || session.action.localizedCaseInsensitiveContains(searchText)
        }
        func matches(_ project: Project) -> Bool {
            project.name.localizedCaseInsensitiveContains(searchText)
                || project.workingDir.localizedCaseInsensitiveContains(searchText)
        }
        func ordered(_ group: [Session]) -> [Session] {
            group.sorted {
                if $0.isArchived != $1.isArchived { return !$0.isArchived }
                return $0.updatedAt > $1.updatedAt
            }
        }

        var visible: [Project] = []
        var grouped: [String: [Session]] = [:]
        for project in projects {
            var members = byProject[project.id] ?? []
            if !searchText.isEmpty && !matches(project) {
                members = members.filter(matches)
                if members.isEmpty { continue }
            }
            visible.append(project)
            grouped[project.id] = ordered(members)
        }

        // Unscoped sessions, plus sessions whose project the archived filter
        // hides (or that vanished server-side) — they must stay reachable.
        var other = byProject[nil] ?? []
        for (projectID, group) in byProject {
            if let projectID, !listedProjectIDs.contains(projectID) {
                other.append(contentsOf: group)
            }
        }
        if !searchText.isEmpty {
            other = other.filter(matches)
        }

        visibleProjects = visible
        sessionsByProject = grouped
        otherSessions = ordered(other)
    }
}

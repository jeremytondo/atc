import SwiftUI
import CockpitAPI

/// Sidebar: one flat list of projects aggregated across all Connections,
/// with sessions nested underneath. Selection is always a `SessionRef` —
/// project rows are disclosure headers that carry the per-project actions
/// (new session, rename, archive). Every project row shows a chip naming
/// its Connection with a reachability dot. There is no fallback bucket:
/// unscoped sessions don't appear, and sessions of archived projects are
/// reachable only through the archived filter.
struct ProjectSidebarView: View {
    @Environment(AppModel.self) private var appModel
    @Environment(\.openSettings) private var openSettings
    @Binding var selection: SessionRef?
    let searchText: String
    let connectedRefs: Set<SessionRef>
    /// The shell presents the New Session sheet for this project.
    @Binding var newSessionContext: NewSessionContext?
    /// The shell presents the New Project sheet.
    var onCreateProject: () -> Void = {}

    /// Groups start expanded; only explicit collapses are remembered.
    @State private var collapsedProjects: Set<ProjectRef> = []
    @State private var renamingRow: SidebarGroups.ProjectRow?
    @State private var renameDraft = ""
    @State private var actionError: String?

    var body: some View {
        @Bindable var appModel = appModel
        // One pass over the runtimes per render; the List body indexes into it.
        let groups = SidebarGroups(
            inputs: appModel.runtimes.map {
                SidebarGroups.ConnectionInput(
                    connection: $0.record,
                    projects: $0.projects.projects,
                    sessions: $0.sessions.sessions
                )
            },
            searchText: searchText
        )
        List(selection: $selection) {
            ForEach(groups.projectRows) { row in
                DisclosureGroup(isExpanded: expansionBinding(row.ref)) {
                    let members = groups.sessions(in: row.ref)
                    ForEach(members) { session in
                        SessionRowView(
                            session: session,
                            isConnected: connectedRefs.contains(
                                SessionRef(connectionID: row.ref.connectionID, sessionID: session.id)
                            ),
                            showsWorkingDir: false
                        )
                        .tag(SessionRef(connectionID: row.ref.connectionID, sessionID: session.id))
                    }
                    if members.isEmpty {
                        Text("No sessions")
                            .font(.caption)
                            .foregroundStyle(.tertiary)
                    }
                } label: {
                    projectLabel(row)
                }
            }
        }
        .safeAreaInset(edge: .top, spacing: 0) {
            HStack {
                Text("Projects")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                Spacer()
                Menu {
                    Toggle("Show Archived", isOn: $appModel.includeArchived)
                } label: {
                    Label("Filter", systemImage: appModel.includeArchived
                        ? "line.3.horizontal.decrease.circle.fill"
                        : "line.3.horizontal.decrease.circle")
                }
                .menuStyle(.borderlessButton)
                .labelStyle(.iconOnly)
                .fixedSize()
                .help("Filter the project list")
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 4)
        }
        .overlay {
            if appModel.runtimes.isEmpty {
                noConnectionsState
            } else if groups.isEmpty && allLoadedOnce {
                emptyState
            }
        }
        .alert("Rename Project", isPresented: Binding(
            get: { renamingRow != nil },
            set: { if !$0 { renamingRow = nil } }
        )) {
            TextField("Name", text: $renameDraft)
            Button("Rename") {
                if let row = renamingRow, let store = projectsStore(for: row) {
                    let trimmed = renameDraft.trimmingCharacters(in: .whitespaces)
                    run { try await store.rename(id: row.project.id, name: trimmed) }
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
    private func projectLabel(_ row: SidebarGroups.ProjectRow) -> some View {
        let project = row.project
        HStack(spacing: 6) {
            Label {
                Text(project.name)
                    .lineLimit(1)
            } icon: {
                Image(systemName: project.isArchived ? "archivebox" : "folder")
            }
            .foregroundStyle(project.isArchived ? AnyShapeStyle(.secondary) : AnyShapeStyle(.primary))
            Spacer(minLength: 4)
            ConnectionChip(
                name: row.connectionName,
                reachability: appModel.reachability(of: row.ref.connectionID)
            )
            if !project.isArchived {
                Button {
                    newSessionContext = NewSessionContext(
                        connectionID: row.ref.connectionID, project: project
                    )
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
                    newSessionContext = NewSessionContext(
                        connectionID: row.ref.connectionID, project: project
                    )
                }
                Button("Rename…", systemImage: "pencil") {
                    renameDraft = project.name
                    renamingRow = row
                }
                Divider()
                // Mirrors the server rule: no archiving with live sessions.
                // A stale view still gets the server's 409 via the alert.
                Button("Archive Project", systemImage: "archivebox") {
                    if let store = projectsStore(for: row) {
                        run { try await store.archive(id: project.id) }
                    }
                }
                .disabled(row.hasActiveSessions)
            } else {
                Button("Unarchive Project", systemImage: "archivebox") {
                    if let store = projectsStore(for: row) {
                        run { try await store.unarchive(id: project.id) }
                    }
                }
            }
        }
        .help(project.workingDir)
    }

    // MARK: - Empty states

    private var noConnectionsState: some View {
        ContentUnavailableView {
            Label("No Connections", systemImage: "network.slash")
        } description: {
            Text("Add a connection to a Cockpit server to see its projects here.")
        } actions: {
            Button("Open Settings") { openSettings() }
        }
    }

    private var emptyState: some View {
        Group {
            if let error = firstError {
                ContentUnavailableView {
                    Label("Can't Reach Cockpit", systemImage: "wifi.exclamationmark")
                } description: {
                    Text(error)
                } actions: {
                    Button("Retry") {
                        Task { await appModel.refreshAll() }
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

    private var allLoadedOnce: Bool {
        appModel.runtimes.allSatisfy {
            $0.projects.hasLoadedOnce && $0.sessions.hasLoadedOnce
        }
    }

    private var firstError: String? {
        for runtime in appModel.runtimes {
            if let error = runtime.projects.lastError ?? runtime.sessions.lastError {
                return "\(runtime.record.name): \(error)"
            }
        }
        return nil
    }

    // MARK: - Helpers

    private func projectsStore(for row: SidebarGroups.ProjectRow) -> ProjectsStore? {
        appModel.runtime(id: row.ref.connectionID)?.projects
    }

    private func expansionBinding(_ ref: ProjectRef) -> Binding<Bool> {
        Binding(
            get: { !collapsedProjects.contains(ref) },
            set: { expanded in
                if expanded {
                    collapsedProjects.remove(ref)
                } else {
                    collapsedProjects.insert(ref)
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

/// Neutral chip naming the Connection a project belongs to, with the
/// reachability dot. The chip never uses color to identify the Connection.
struct ConnectionChip: View {
    let name: String
    let reachability: Reachability

    var body: some View {
        HStack(spacing: 4) {
            Circle()
                .fill(reachability.color)
                .frame(width: 6, height: 6)
            Text(name)
                .font(.caption2)
                .foregroundStyle(.secondary)
                .lineLimit(1)
        }
        .padding(.horizontal, 6)
        .padding(.vertical, 2)
        .background(.quaternary.opacity(0.5), in: Capsule())
    }
}

/// The sidebar's grouping/filtering across Connections, computed once per
/// render. Rows are ordered by Connection creation order, then each
/// Connection's server ordering. Search rules: a project whose own name or
/// directory matches shows all its sessions; otherwise it stays visible only
/// if some of its sessions match, filtered down to those. Within a group,
/// active sessions come first (newest activity), archived ones trail.
/// Unscoped sessions and sessions of unlisted projects are dropped — the
/// server guarantees active sessions can't be orphaned by archiving.
struct SidebarGroups {
    struct ConnectionInput {
        let connection: ConnectionRecord
        let projects: [Project]
        let sessions: [Session]
    }

    struct ProjectRow: Identifiable {
        let ref: ProjectRef
        let project: Project
        let connectionName: String
        /// Any known session is `starting`/`running` — gates Archive.
        let hasActiveSessions: Bool
        var id: ProjectRef { ref }
    }

    private(set) var projectRows: [ProjectRow] = []
    private var sessionsByProject: [ProjectRef: [Session]] = [:]

    var isEmpty: Bool { projectRows.isEmpty }

    func sessions(in ref: ProjectRef) -> [Session] {
        sessionsByProject[ref] ?? []
    }

    init(inputs: [ConnectionInput], searchText: String) {
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

        for input in inputs {
            let byProject = Dictionary(grouping: input.sessions, by: { $0.project?.id })
            for project in input.projects {
                let allMembers = byProject[project.id] ?? []
                var members = allMembers
                if !searchText.isEmpty && !matches(project) {
                    members = members.filter(matches)
                    if members.isEmpty { continue }
                }
                let ref = ProjectRef(connectionID: input.connection.id, projectID: project.id)
                projectRows.append(ProjectRow(
                    ref: ref,
                    project: project,
                    connectionName: input.connection.name,
                    hasActiveSessions: allMembers.contains {
                        $0.status == .starting || $0.status == .running
                    }
                ))
                sessionsByProject[ref] = ordered(members)
            }
        }
    }
}

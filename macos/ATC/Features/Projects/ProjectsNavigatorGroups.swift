import Foundation
import ATCAPI

/// Pure, deterministic projection shared by the Projects Navigator and the
/// Workspace Switcher.
struct ProjectsNavigatorGroups {
    struct Input {
        let connection: ConnectionRecord
        let reachability: Reachability
        let projects: [Project]
        let workspaces: [Workspace]
        let sessions: [Session]
    }

    struct WorkspaceRow: Identifiable {
        let ref: WorkspaceRef
        let workspace: Workspace
        let hasActiveSessions: Bool
        let sessionCount: Int
        let activeSessionCount: Int
        var id: WorkspaceRef { ref }
    }

    struct ProjectGroup: Identifiable {
        let ref: ProjectRef
        let project: Project
        let connectionName: String
        let reachability: Reachability
        let workspaces: [WorkspaceRow]
        let totalWorkspaceCount: Int
        let hasUnarchivedWorkspaces: Bool
        var id: ProjectRef { ref }
    }

    let projects: [ProjectGroup]

    /// The one projection from live runtimes shared by the Projects
    /// Navigator and the Workspace Switcher.
    init(runtimes: [ConnectionRuntime]) {
        self.init(inputs: runtimes.map {
            Input(
                connection: $0.record,
                reachability: $0.reachability,
                projects: $0.projects.projects,
                workspaces: $0.workspaces.workspaces,
                sessions: $0.sessions.sessions
            )
        })
    }

    init(inputs: [Input]) {
        projects = inputs.flatMap { input -> [ProjectGroup] in
            let allWorkspaces = Dictionary(grouping: input.workspaces, by: \.projectId)
            let sessions = Dictionary(grouping: input.sessions, by: { $0.workspace?.id })
            return input.projects.compactMap { project -> ProjectGroup? in
                guard !project.isArchived else { return nil }
                let all = allWorkspaces[project.id] ?? []
                let rows = all
                    .filter { !$0.isArchived }
                    .sortedNewestFirst()
                    .map { workspace in
                        let members = sessions[workspace.id] ?? []
                        return WorkspaceRow(
                            ref: WorkspaceRef(
                                connectionID: input.connection.id,
                                workspaceID: workspace.id
                            ),
                            workspace: workspace,
                            hasActiveSessions: members.contains(where: \.isActive),
                            sessionCount: members.count,
                            activeSessionCount: members.filter(\.isActive).count
                        )
                    }
                return ProjectGroup(
                    ref: ProjectRef(
                        connectionID: input.connection.id,
                        projectID: project.id
                    ),
                    project: project,
                    connectionName: input.connection.name,
                    reachability: input.reachability,
                    workspaces: rows,
                    totalWorkspaceCount: all.count,
                    hasUnarchivedWorkspaces: all.contains { !$0.isArchived }
                )
            }
        }
        .sorted(by: Self.projectComesFirst)
    }

    nonisolated private static func projectComesFirst(
        _ lhs: ProjectGroup,
        _ rhs: ProjectGroup
    ) -> Bool {
        let name = lhs.project.name.localizedCaseInsensitiveCompare(rhs.project.name)
        if name != .orderedSame { return name == .orderedAscending }
        let connection = lhs.connectionName.localizedCaseInsensitiveCompare(rhs.connectionName)
        if connection != .orderedSame { return connection == .orderedAscending }
        let connectionID = lhs.ref.connectionID.uuidString.compare(rhs.ref.connectionID.uuidString)
        if connectionID != .orderedSame { return connectionID == .orderedAscending }
        return lhs.ref.projectID < rhs.ref.projectID
    }
}

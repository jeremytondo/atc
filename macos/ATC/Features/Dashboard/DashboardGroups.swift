import Foundation
import ATCAPI

/// The Dashboard's grouping across Connections, computed once per render
/// (mirrors the old `SidebarGroups` pattern). Ordering rules:
///
/// - Sections follow Connection creation order (the runtimes array).
/// - Project cards follow each Connection's server ordering.
/// - Workspace rows are newest-created-first within their card.
///
/// Session inputs are joined by workspace ID to fill the delete
/// confirmation's local-store counts.
struct DashboardGroups {
    struct ConnectionInput {
        let connection: ConnectionRecord
        let projects: [Project]
        let workspaces: [Workspace]
        let sessions: [Session]
    }

    struct WorkspaceRow: Identifiable {
        let ref: WorkspaceRef
        let workspace: Workspace
        let hasActiveSessions: Bool
        /// Local-store counts for the delete confirmation.
        let sessionCount: Int
        let activeSessionCount: Int
        var id: WorkspaceRef { ref }
    }

    struct ProjectCard: Identifiable {
        let ref: ProjectRef
        let project: Project
        let rows: [WorkspaceRow]
        /// Counts every Workspace — gates Delete Project (zero required).
        let totalWorkspaceCount: Int
        var id: ProjectRef { ref }
    }

    struct Section: Identifiable {
        let connectionID: UUID
        let connectionName: String
        /// Local/remote context ("Local" or the remote host).
        let contextLabel: String
        let cards: [ProjectCard]
        var id: UUID { connectionID }
    }

    private(set) var sections: [Section] = []

    /// Every project across all Connections.
    private(set) var totalProjectCount = 0

    /// True when no section has any visible card.
    var isEmpty: Bool { sections.allSatisfy(\.cards.isEmpty) }

    /// Visible Workspace refs in display order, for keyboard navigation.
    var workspaceRefs: [WorkspaceRef] {
        sections.flatMap { $0.cards.flatMap { $0.rows.map(\.ref) } }
    }

    init(inputs: [ConnectionInput]) {
        for input in inputs {
            totalProjectCount += input.projects.count
            let workspacesByProject = Dictionary(grouping: input.workspaces, by: \.projectId)
            let sessionsByWorkspace = Dictionary(grouping: input.sessions, by: { $0.workspace?.id })
            var cards: [ProjectCard] = []
            for project in input.projects {
                let all = workspacesByProject[project.id] ?? []
                let rows = all
                    .sortedNewestFirst()
                    .map { workspace in
                        let members = sessionsByWorkspace[workspace.id] ?? []
                        let active = members.filter(\.isActive)
                        return WorkspaceRow(
                            ref: WorkspaceRef(
                                connectionID: input.connection.id,
                                workspaceID: workspace.id
                            ),
                            workspace: workspace,
                            hasActiveSessions: !active.isEmpty,
                            sessionCount: members.count,
                            activeSessionCount: active.count
                        )
                    }
                cards.append(ProjectCard(
                    ref: ProjectRef(connectionID: input.connection.id, projectID: project.id),
                    project: project,
                    rows: rows,
                    totalWorkspaceCount: all.count
                ))
            }
            sections.append(Section(
                connectionID: input.connection.id,
                connectionName: input.connection.name,
                contextLabel: ConnectionURL.contextLabel(for: input.connection.urlString),
                cards: cards
            ))
        }
    }
}

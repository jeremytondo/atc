// The sidebar's cross-Connection projection, computed once per render.
//
// Two rules live here rather than in the view because they are global, not
// per-row: a Project label carries its Connection name only when another
// Connection has a Project by the same name, and a Thread shows its working
// directory only when it differs from its Project's default. Both need the
// whole picture, so they are decided once, here.
//
// Ordering is the design's: pins oldest-pin-first (a stable shelf), the
// filtered thread list newest-created-first, archived newest-archived-first.

import ATCAppServerAPI
import Foundation

/// One thread as the sidebar and the command palette present it.
struct ThreadListItem: Identifiable, Equatable {
    let ref: ThreadRef
    let thread: ATCThread
    /// Project name, suffixed with the Connection name when ambiguous.
    let projectLabel: String
    /// Working directory, present only when it differs from the Project's
    /// default — an identical path is noise on every card.
    let distinctWorkingDirectory: String?

    var id: ThreadRef { ref }
}

/// One project as the filter menu and the New Thread picker present it.
struct ProjectOption: Identifiable, Equatable {
    let ref: ProjectRef
    let project: Project
    let connectionName: String
    /// Project name, suffixed with the Connection name when ambiguous.
    let label: String

    var id: ProjectRef { ref }
}

struct ThreadListModel {
    struct ConnectionInput {
        let connectionID: UUID
        let connectionName: String
        let projects: [Project]
        let threads: [ATCThread]
        let archivedThreads: [ATCThread]
    }

    let projects: [ProjectOption]
    /// Global, oldest pin first; unaffected by the filter.
    let pinned: [ThreadListItem]
    /// Filtered, non-archived, non-pinned; newest created first.
    let recent: [ThreadListItem]
    /// Global, newest archived first.
    let archived: [ThreadListItem]

    init(inputs: [ConnectionInput], filter: ThreadFilter) {
        var nameCounts: [String: Int] = [:]
        for input in inputs {
            for project in input.projects {
                nameCounts[project.name, default: 0] += 1
            }
        }

        var options: [ProjectOption] = []
        var projectsByRef: [ProjectRef: Project] = [:]
        var labelsByRef: [ProjectRef: String] = [:]
        for input in inputs {
            for project in input.projects {
                let ref = ProjectRef(connectionID: input.connectionID, projectID: project.id)
                let label = nameCounts[project.name, default: 0] > 1
                    ? "\(project.name) · \(input.connectionName)"
                    : project.name
                projectsByRef[ref] = project
                labelsByRef[ref] = label
                options.append(ProjectOption(
                    ref: ref,
                    project: project,
                    connectionName: input.connectionName,
                    label: label
                ))
            }
        }
        projects = options

        func item(_ thread: ATCThread, connectionID: UUID) -> ThreadListItem {
            let projectRef = ProjectRef(connectionID: connectionID, projectID: thread.projectId)
            let project = projectsByRef[projectRef]
            return ThreadListItem(
                ref: ThreadRef(connectionID: connectionID, threadID: thread.id),
                thread: thread,
                projectLabel: labelsByRef[projectRef] ?? "Unknown Project",
                distinctWorkingDirectory: thread.workingDirectory == project?.defaultWorkingDirectory
                    ? nil
                    : thread.workingDirectory
            )
        }

        var pinnedItems: [ThreadListItem] = []
        var recentItems: [ThreadListItem] = []
        var archivedItems: [ThreadListItem] = []
        for input in inputs {
            for thread in input.threads where !thread.isArchived {
                if thread.isPinned {
                    pinnedItems.append(item(thread, connectionID: input.connectionID))
                    continue
                }
                guard Self.matches(thread, connectionID: input.connectionID, filter: filter) else {
                    continue
                }
                recentItems.append(item(thread, connectionID: input.connectionID))
            }
            for thread in input.archivedThreads {
                archivedItems.append(item(thread, connectionID: input.connectionID))
            }
        }

        pinned = pinnedItems.sorted {
            ($0.thread.pinnedAt ?? .distantPast) < ($1.thread.pinnedAt ?? .distantPast)
        }
        recent = recentItems.sorted { $0.thread.createdAt > $1.thread.createdAt }
        archived = archivedItems.sorted {
            ($0.thread.archivedAt ?? .distantPast) > ($1.thread.archivedAt ?? .distantPast)
        }
    }

    private static func matches(
        _ thread: ATCThread,
        connectionID: UUID,
        filter: ThreadFilter
    ) -> Bool {
        switch filter {
        case .all:
            true
        case .project(let ref):
            ref.connectionID == connectionID && ref.projectID == thread.projectId
        case .archived:
            false
        }
    }
}

extension ThreadListModel.ConnectionInput {
    @MainActor
    init(runtime: ConnectionRuntime) {
        self.init(
            connectionID: runtime.id,
            connectionName: runtime.record.name,
            projects: runtime.projects.projects,
            threads: runtime.threads.threads,
            archivedThreads: runtime.threads.archivedThreads
        )
    }
}

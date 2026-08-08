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
    /// The Connection's display name, shown on every card per the design.
    let connectionName: String
    /// Working directory, present only when it differs from the Project's
    /// default — an identical path is noise on every card.
    let distinctWorkingDirectory: String?

    /// Row identity is section-scoped, not the bare `ThreadRef`. The pinned,
    /// recent, and archived sections are sibling `ForEach`es inside one
    /// `LazyVStack`, whose item cache is keyed by id across the whole stack:
    /// with a ref-only id, pin/unpin/archive reads as a *move* of one row
    /// between sections and the lazy layout keeps serving the stale cached
    /// view — a card that neither updates nor responds until relaunch.
    /// Folding the section-determining flags into the id turns those
    /// transitions into remove-plus-insert of distinct rows.
    struct ID: Hashable {
        let ref: ThreadRef
        let isPinned: Bool
        let isArchived: Bool
    }

    var id: ID { ID(ref: ref, isPinned: thread.isPinned, isArchived: thread.isArchived) }
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

        func item(_ thread: ATCThread, input: ConnectionInput) -> ThreadListItem {
            let projectRef = ProjectRef(connectionID: input.connectionID, projectID: thread.projectId)
            let project = projectsByRef[projectRef]
            return ThreadListItem(
                ref: ThreadRef(connectionID: input.connectionID, threadID: thread.id),
                thread: thread,
                projectLabel: labelsByRef[projectRef] ?? "Unknown Project",
                connectionName: input.connectionName,
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
                    pinnedItems.append(item(thread, input: input))
                    continue
                }
                guard Self.matches(thread, connectionID: input.connectionID, filter: filter) else {
                    continue
                }
                recentItems.append(item(thread, input: input))
            }
            for thread in input.archivedThreads {
                archivedItems.append(item(thread, input: input))
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

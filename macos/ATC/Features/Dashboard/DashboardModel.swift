// The Dashboard's grouping across Connections, computed once per render.
//
// Counts are the whole point of a card, and they are all derived here so the
// view never re-walks the thread list per project: active threads, standalone
// terminals (a thread's TUI terminal is part of the thread, not a separate
// line item), and threads waiting on the user.

import ATCAppServerAPI
import Foundation

struct DashboardModel {
    struct ConnectionInput {
        let connectionID: UUID
        let connectionName: String
        let urlString: String
        let projects: [Project]
        let threads: [ATCThread]
        let terminals: [Terminal]
    }

    struct ProjectCard: Identifiable {
        let ref: ProjectRef
        let project: Project
        let activeThreadCount: Int
        let standaloneTerminalCount: Int
        let needsInputCount: Int

        var id: ProjectRef { ref }
    }

    struct Section: Identifiable {
        let connectionID: UUID
        let connectionName: String
        /// "Local" or the remote host.
        let contextLabel: String
        let cards: [ProjectCard]

        var id: UUID { connectionID }
    }

    private(set) var sections: [Section] = []
    private(set) var totalProjectCount = 0

    init(inputs: [ConnectionInput]) {
        for input in inputs {
            totalProjectCount += input.projects.count
            let threadsByProject = Dictionary(grouping: input.threads.filter { !$0.isArchived }) {
                $0.projectId
            }
            let standaloneByProject = Dictionary(grouping: input.terminals.filter { $0.threadId == nil }) {
                $0.projectId
            }
            let cards = input.projects.map { project in
                let threads = threadsByProject[project.id] ?? []
                return ProjectCard(
                    ref: ProjectRef(connectionID: input.connectionID, projectID: project.id),
                    project: project,
                    activeThreadCount: threads.count,
                    standaloneTerminalCount: (standaloneByProject[project.id] ?? []).count,
                    needsInputCount: threads.filter { $0.activityState == .needsInput }.count
                )
            }
            sections.append(
                Section(
                    connectionID: input.connectionID,
                    connectionName: input.connectionName,
                    contextLabel: ConnectionURL.contextLabel(for: input.urlString),
                    cards: cards
                ))
        }
    }
}

extension DashboardModel.ConnectionInput {
    @MainActor
    init(runtime: ConnectionRuntime) {
        self.init(
            connectionID: runtime.id,
            connectionName: runtime.record.name,
            urlString: runtime.record.urlString,
            projects: runtime.projects.projects,
            threads: runtime.threads.threads,
            terminals: runtime.terminals.terminals
        )
    }
}

import SwiftUI
import ATCAPI

struct WorkspaceSwitcher: View {
    @Environment(AppModel.self) private var appModel
    @Environment(WindowState.self) private var windowState

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
        let presentation = activeContext.map {
            WorkspaceSwitcherPresentation(
                project: $0.project,
                workspace: $0.workspace,
                connectionName: $0.runtime.record.name,
                reachability: $0.runtime.reachability
            )
        } ?? .noActiveWorkspace
        Menu {
            if let context = activeContext, context.workspace.isArchived {
                Section("Current Workspace") {
                    Text("\(context.project.name) › \(context.workspace.name) — Archived")
                }
            }
            ForEach(groups.projects) { project in
                Section("\(project.project.name) — \(project.connectionName)") {
                    ForEach(project.workspaces) { row in
                        Button {
                            _ = windowState.activateWorkspace(row.ref, in: appModel)
                        } label: {
                            if row.ref == windowState.activeWorkspace {
                                Label(row.workspace.name, systemImage: "checkmark")
                            } else {
                                Text(row.workspace.name)
                            }
                        }
                        .disabled(project.reachability != .connected)
                    }
                }
            }
            if groups.projects.allSatisfy({ $0.workspaces.isEmpty }) {
                Text("No Workspaces")
            }
        } label: {
            HStack(spacing: Spacing.sm) {
                if let context = activeContext {
                    StatusDot(color: context.runtime.reachability.color)
                    Text(presentation.label)
                        .lineLimit(1)
                } else {
                    Text(presentation.label)
                }
            }
        }
        .help(presentation.help)
        .accessibilityLabel(presentation.help)
    }

    private var activeContext: (
        runtime: ConnectionRuntime,
        project: Project,
        workspace: Workspace
    )? {
        guard let ref = windowState.activeWorkspace,
              let runtime = appModel.runtime(id: ref.connectionID),
              let workspace = runtime.workspaces.workspace(id: ref.workspaceID),
              let project = runtime.projects.project(id: workspace.projectId)
        else { return nil }
        return (runtime, project, workspace)
    }
}

struct WorkspaceSwitcherPresentation: Equatable {
    let label: String
    let help: String
    let isArchived: Bool

    static let noActiveWorkspace = WorkspaceSwitcherPresentation(
        label: "Select Workspace…",
        help: "Select an Active Workspace",
        isArchived: false
    )

    private init(label: String, help: String, isArchived: Bool) {
        self.label = label
        self.help = help
        self.isArchived = isArchived
    }

    init(
        project: Project,
        workspace: Workspace,
        connectionName: String,
        reachability: Reachability
    ) {
        label = "\(project.name) › \(workspace.name)"
        let status = reachability == .connected ? "Connected" : "Disconnected"
        let archived = workspace.isArchived ? ", Archived" : ""
        help = "\(label), \(connectionName), \(status)\(archived)"
        isArchived = workspace.isArchived
    }
}

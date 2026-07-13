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
            HStack(spacing: 6) {
                if let context = activeContext {
                    Circle()
                        .fill(context.runtime.reachability.color)
                        .frame(width: 7, height: 7)
                    Text("\(context.project.name) › \(context.workspace.name)")
                        .lineLimit(1)
                } else {
                    Text("Select Workspace…")
                }
                Image(systemName: "chevron.down")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            }
        }
        .help(helpText)
        .accessibilityLabel(helpText)
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

    private var helpText: String {
        guard let context = activeContext else { return "Select an Active Workspace" }
        let status = context.runtime.reachability == .connected ? "Connected" : "Disconnected"
        let archived = context.workspace.isArchived ? ", Archived" : ""
        return "\(context.project.name) › \(context.workspace.name), \(context.runtime.record.name), \(status)\(archived)"
    }
}

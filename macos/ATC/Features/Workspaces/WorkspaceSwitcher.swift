import SwiftUI
import ATCAPI

/// The toolbar's workspace pill: project › workspace breadcrumb plus the
/// visible session's kind. A plain Button + popover, NOT a Menu — toolbar
/// Menus bridge to a native item that flattens custom labels to text, so
/// composite content (the badge) never renders inside one.
struct WorkspaceSwitcher: View {
    @Environment(AppModel.self) private var appModel
    @Environment(WindowState.self) private var windowState

    @State private var isPickerPresented = false

    var body: some View {
        let presentation = activeContext.map {
            WorkspaceSwitcherPresentation(
                project: $0.project,
                workspace: $0.workspace
            )
        } ?? .noActiveWorkspace
        Button {
            isPickerPresented.toggle()
        } label: {
            HStack(spacing: Spacing.sm) {
                Text(presentation.label)
                    .lineLimit(1)
                Image(systemName: "chevron.down")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                if let sessionBadgeLabel {
                    TagBadge(text: sessionBadgeLabel)
                }
            }
            .padding(.horizontal, Spacing.md)
            .padding(.vertical, Spacing.xs)
            .glassEffect()
            .contentShape(Capsule())
        }
        .buttonStyle(.plain)
        .popover(isPresented: $isPickerPresented, arrowEdge: .bottom) {
            // An archived Active Workspace never appears in the picker's
            // rows, so name it explicitly.
            WorkspacePicker(archivedCurrent: activeContext.flatMap {
                $0.workspace.isArchived ? presentation.label : nil
            })
        }
        .help(presentation.help)
        .accessibilityLabel(
            sessionBadgeLabel.map { "\(presentation.help), \($0)" } ?? presentation.help
        )
    }

    /// What launched the visible session ("Claude", "Terminal", …); nil
    /// whenever no session is the selected content.
    private var sessionBadgeLabel: String? {
        guard let ref = windowState.selectedSession,
              let session = appModel.session(for: ref)
        else { return nil }
        let actions = appModel.runtime(id: ref.connectionID)?.actions.actions ?? []
        return SessionKind.actionLabel(session: session, actions: actions)
    }

    private var activeContext: (project: Project, workspace: Workspace)? {
        guard let ref = windowState.activeWorkspace,
              let runtime = appModel.runtime(id: ref.connectionID),
              let workspace = runtime.workspaces.workspace(id: ref.workspaceID),
              let project = runtime.projects.project(id: workspace.projectId)
        else { return nil }
        return (project, workspace)
    }
}

/// Popover body: the same grouped Workspace chooser the old Menu offered,
/// with the filter field the pill design calls for.
private struct WorkspacePicker: View {
    @Environment(AppModel.self) private var appModel
    @Environment(WindowState.self) private var windowState
    @Environment(\.dismiss) private var dismiss

    /// "project › workspace" when the Active Workspace is archived (and
    /// therefore absent from the rows below); nil otherwise.
    let archivedCurrent: String?

    @State private var query = ""

    var body: some View {
        let groups = filteredGroups()
        VStack(spacing: 0) {
            TextField("Filter Workspaces", text: $query)
                .textFieldStyle(.roundedBorder)
                .padding(Spacing.sm)
            Divider()
            if let archivedCurrent {
                Text("\(archivedCurrent) — Archived")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .padding(Spacing.sm)
                Divider()
            }
            if groups.isEmpty {
                Text(query.isEmpty ? "No Workspaces" : "No Matches")
                    .foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                List {
                    ForEach(groups, id: \.group.id) { entry in
                        Section("\(entry.group.project.name) — \(entry.group.connectionName)") {
                            ForEach(entry.rows) { row in
                                workspaceRow(row, in: entry.group)
                            }
                        }
                    }
                }
                .listStyle(.sidebar)
                .scrollContentBackground(.hidden)
            }
        }
        .frame(width: 300, height: 360)
    }

    private func workspaceRow(
        _ row: ProjectsNavigatorGroups.WorkspaceRow,
        in group: ProjectsNavigatorGroups.ProjectGroup
    ) -> some View {
        Button {
            _ = windowState.activateWorkspace(row.ref, in: appModel)
            dismiss()
        } label: {
            HStack {
                Text(row.workspace.name)
                    .lineLimit(1)
                Spacer()
                if row.ref == windowState.activeWorkspace {
                    Image(systemName: "checkmark")
                        .foregroundStyle(.secondary)
                }
            }
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .disabled(group.reachability != .connected)
    }

    private func filteredGroups() -> [(
        group: ProjectsNavigatorGroups.ProjectGroup,
        rows: [ProjectsNavigatorGroups.WorkspaceRow]
    )] {
        ProjectsNavigatorGroups(runtimes: appModel.runtimes).projects.compactMap { group in
            let rows = query.isEmpty ? group.workspaces : group.workspaces.filter {
                $0.workspace.name.localizedCaseInsensitiveContains(query)
                    || group.project.name.localizedCaseInsensitiveContains(query)
            }
            return rows.isEmpty ? nil : (group, rows)
        }
    }
}

struct WorkspaceSwitcherPresentation: Equatable {
    let label: String
    let help: String

    static let noActiveWorkspace = WorkspaceSwitcherPresentation(
        label: "Select Workspace…",
        help: "Select an Active Workspace"
    )

    private init(label: String, help: String) {
        self.label = label
        self.help = help
    }

    init(project: Project, workspace: Workspace) {
        label = "\(project.name) › \(workspace.name)"
        help = workspace.isArchived ? "\(label), Archived" : label
    }
}

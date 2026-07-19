import SwiftUI
import ATCAPI

/// The toolbar's workspace pill, styled like Xcode's scheme picker: a
/// static project prefix, then the workspace name as the one interactive
/// segment — it alone highlights on hover and reveals the dropdown
/// chevron. A plain Button + popover, NOT a Menu — toolbar Menus bridge
/// to a native item that flattens custom labels to text, so composite
/// content (the badge) never renders inside one. The toolbar already
/// wraps the item in Liquid Glass, so the label draws no container of
/// its own — a second glassEffect here nests two capsule outlines.
struct WorkspaceSwitcher: View {
    @Environment(AppModel.self) private var appModel
    @Environment(WindowState.self) private var windowState

    @State private var isPickerPresented = false
    @State private var isHovering = false

    var body: some View {
        let presentation = activeContext.map {
            WorkspaceSwitcherPresentation(
                project: $0.project,
                workspace: $0.workspace
            )
        } ?? .noActiveWorkspace
        // Highlight persists while the picker is open, matching Xcode.
        let isHighlighted = isHovering || isPickerPresented
        HStack(spacing: Spacing.xs) {
            if let project = presentation.projectName {
                Text(project)
                    .foregroundStyle(.secondary)
                Text("›")
                    .foregroundStyle(.tertiary)
            }
            Button {
                isPickerPresented.toggle()
            } label: {
                HStack(spacing: Spacing.xs) {
                    Text(presentation.workspaceName)
                        .fontWeight(.medium)
                    // Opacity, not removal: the reserved width keeps the
                    // pill from shifting as the chevron fades in.
                    Image(systemName: "chevron.down")
                        .font(.system(size: 9, weight: .semibold))
                        .foregroundStyle(.tertiary)
                        .opacity(isHighlighted ? 1 : 0)
                }
                .padding(.horizontal, Spacing.sm)
                .padding(.vertical, 2)
                .background(
                    .quaternary.opacity(isHighlighted ? 1 : 0),
                    in: RoundedRectangle(cornerRadius: 5)
                )
                .contentShape(RoundedRectangle(cornerRadius: 5))
            }
            .buttonStyle(.plain)
            .onHover { isHovering = $0 }
            .popover(isPresented: $isPickerPresented, arrowEdge: .bottom) {
                // An archived Active Workspace never appears in the
                // picker's rows, so name it explicitly.
                WorkspacePicker(archivedCurrent: activeContext.flatMap {
                    $0.workspace.isArchived ? presentation.label : nil
                })
            }
            .help(presentation.help)
            .accessibilityLabel(
                sessionBadgeLabel.map { "\(presentation.help), \($0)" }
                    ?? presentation.help
            )
            if let sessionBadgeLabel {
                TagBadge(text: sessionBadgeLabel)
            }
        }
        .lineLimit(1)
        // The toolbar offers custom principal views less than their
        // natural width, which SwiftUI resolves by squeezing the Texts
        // into "…" — even short names. fixedSize makes the pill's
        // intrinsic width non-negotiable.
        .fixedSize()
        // Inset the content from the toolbar item's glass capsule so the
        // hover highlight and badge never crowd its rounded ends.
        .padding(.horizontal, Spacing.md)
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
            // Xcode-style filter: the glyph and a plain field inside one
            // capsule inset, even margins, no divider below.
            HStack(spacing: Spacing.xs) {
                Image(systemName: "line.3.horizontal.decrease.circle")
                    .foregroundStyle(.secondary)
                TextField("Filter", text: $query)
                    .textFieldStyle(.plain)
            }
            .padding(.horizontal, Spacing.sm)
            .padding(.vertical, 6)
            .background(.quaternary, in: Capsule())
            .padding(Spacing.md)
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
    /// nil when no Active Workspace is selected — the pill then shows
    /// only `workspaceName` (the placeholder prompt).
    let projectName: String?
    let workspaceName: String
    let help: String

    /// Flat "project › workspace" string for accessibility and the
    /// archived-current row in the picker.
    var label: String {
        projectName.map { "\($0) › \(workspaceName)" } ?? workspaceName
    }

    static let noActiveWorkspace = WorkspaceSwitcherPresentation(
        projectName: nil,
        workspaceName: "Select Workspace…",
        help: "Select an Active Workspace"
    )

    private init(projectName: String?, workspaceName: String, help: String) {
        self.projectName = projectName
        self.workspaceName = workspaceName
        self.help = help
    }

    init(project: Project, workspace: Workspace) {
        projectName = project.name
        workspaceName = workspace.name
        let label = "\(project.name) › \(workspace.name)"
        help = workspace.isArchived ? "\(label), Archived" : label
    }
}

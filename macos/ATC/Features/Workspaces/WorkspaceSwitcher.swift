import SwiftUI
import ATCAPI

/// The toolbar's continuous context pill. Plain Buttons + popovers are used
/// instead of toolbar Menus because native toolbar Menu labels flatten
/// composite SwiftUI content such as the Session index badge.
struct WorkspaceSwitcher: View {
    @Environment(AppModel.self) private var appModel
    @Environment(WindowState.self) private var windowState

    @State private var isWorkspacePickerPresented = false
    @State private var isSessionPickerPresented = false
    @State private var isWorkspaceHovering = false
    @State private var isSessionHovering = false

    var body: some View {
        let presentation = activeContext.map {
            WorkspaceSwitcherPresentation(
                project: $0.project,
                workspace: $0.workspace,
                session: selectedSession
            )
        } ?? .noActiveWorkspace

        HStack(spacing: 0) {
            workspaceRegion(presentation)
            if let session = presentation.session {
                sessionRegion(session, help: presentation.help)
            }
        }
        .lineLimit(1)
        .padding(.horizontal, Spacing.md)
    }

    private func workspaceRegion(
        _ presentation: WorkspaceSwitcherPresentation
    ) -> some View {
        let isHighlighted = isWorkspaceHovering || isWorkspacePickerPresented
        return Button {
            isWorkspacePickerPresented.toggle()
        } label: {
            HStack(spacing: Spacing.xs) {
                if let project = presentation.projectName {
                    Text(project)
                        .foregroundStyle(.secondary)
                        .layoutPriority(1)
                    Text("›")
                        .foregroundStyle(.tertiary)
                }
                Text(presentation.workspaceName)
                    .fontWeight(.medium)
                    .layoutPriority(3)
                regionChevron(isHighlighted: isHighlighted)
            }
            .regionChrome(isHighlighted: isHighlighted)
        }
        .buttonStyle(.plain)
        .onHover { isWorkspaceHovering = $0 }
        .popover(isPresented: $isWorkspacePickerPresented, arrowEdge: .bottom) {
            WorkspacePicker()
        }
        .help(presentation.workspaceHelp)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("Choose Workspace, \(presentation.label)")
    }

    private func sessionRegion(
        _ identity: SessionIdentity,
        help: String
    ) -> some View {
        let isHighlighted = isSessionHovering || isSessionPickerPresented
        return Button {
            isSessionPickerPresented.toggle()
        } label: {
            HStack(spacing: Spacing.xs) {
                Text("›")
                    .foregroundStyle(.tertiary)
                if let index = identity.index {
                    SessionIndexBadge(index)
                        .layoutPriority(4)
                }
                Text(identity.identityText)
                    .fontWeight(.medium)
                    .layoutPriority(4)
                if let customName = identity.customName {
                    Text("·")
                        .foregroundStyle(.tertiary)
                        .layoutPriority(2)
                    Text(customName)
                        .foregroundStyle(.secondary)
                        .layoutPriority(2)
                }
                regionChevron(isHighlighted: isHighlighted)
            }
            .regionChrome(isHighlighted: isHighlighted)
        }
        .buttonStyle(.plain)
        .onHover { isSessionHovering = $0 }
        .popover(isPresented: $isSessionPickerPresented, arrowEdge: .bottom) {
            WorkspaceSessionPicker()
        }
        .help(help)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("Choose Session, \(identity.accessibilityLabel)")
    }

    private func regionChevron(isHighlighted: Bool) -> some View {
        Image(systemName: "chevron.down")
            .font(.system(size: 9, weight: .semibold))
            .foregroundStyle(.tertiary)
            .opacity(isHighlighted ? 1 : 0)
    }

    private var selectedSession: Session? {
        guard let workspace = windowState.activeWorkspace,
              let ref = windowState.selectedSession,
              ref.connectionID == workspace.connectionID,
              let session = appModel.session(for: ref),
              session.belongs(to: workspace)
        else { return nil }
        return session
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

/// Pure projection for the Workspace-scoped Session picker.
struct SessionPickerPresentation: Equatable, Sendable {
    let groups: WorkspaceSessionGroups
    let selectedSession: SessionRef?

    init(
        workspace: WorkspaceRef,
        sessions: [Session],
        selectedSession: SessionRef?
    ) {
        groups = WorkspaceSessionGroups(workspace: workspace, sessions: sessions)
        self.selectedSession = selectedSession
    }

    func isSelected(_ row: WorkspaceSessionGroups.Row) -> Bool {
        row.ref == selectedSession
    }
}

/// Picker for existing Sessions and Terminals in the Active Workspace.
/// Selection is immediate; creation and filtering intentionally live
/// elsewhere.
struct WorkspaceSessionPicker: View {
    @Environment(AppModel.self) private var appModel
    @Environment(WindowState.self) private var windowState
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        if let presentation {
            List {
                sessionSection(
                    "Sessions",
                    rows: presentation.groups.sessions,
                    emptyText: "No Sessions",
                    presentation: presentation
                )
                sessionSection(
                    "Terminals",
                    rows: presentation.groups.terminals,
                    emptyText: "No Terminals",
                    presentation: presentation
                )
            }
            .listStyle(.sidebar)
            .scrollContentBackground(.hidden)
            .frame(width: 300, height: 300)
        } else {
            Text("No Active Workspace")
                .foregroundStyle(.secondary)
                .frame(width: 300, height: 160)
        }
    }

    @ViewBuilder
    private func sessionSection(
        _ title: String,
        rows: [WorkspaceSessionGroups.Row],
        emptyText: String,
        presentation: SessionPickerPresentation
    ) -> some View {
        Section(title) {
            if rows.isEmpty {
                Text(emptyText)
                    .font(.caption)
                    .foregroundStyle(.tertiary)
            } else {
                ForEach(rows) { row in
                    Button {
                        if windowState.selectSession(row.ref, in: appModel) {
                            dismiss()
                        }
                    } label: {
                        HStack(spacing: Spacing.xs) {
                            if let index = row.identity.index {
                                SessionIndexBadge(index)
                            }
                            Text(row.identity.fullLabel)
                                .lineLimit(1)
                            Spacer()
                            if presentation.isSelected(row) {
                                Image(systemName: "checkmark")
                                    .foregroundStyle(.secondary)
                            }
                        }
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .help(row.identity.indexedLabel)
                    .accessibilityElement(children: .ignore)
                    .accessibilityLabel(row.identity.accessibilityLabel)
                    .accessibilityAddTraits(
                        presentation.isSelected(row) ? .isSelected : []
                    )
                }
            }
        }
    }

    private var presentation: SessionPickerPresentation? {
        guard let workspace = windowState.activeWorkspace,
              let runtime = appModel.runtime(id: workspace.connectionID)
        else { return nil }
        return SessionPickerPresentation(
            workspace: workspace,
            sessions: runtime.sessions.sessions,
            selectedSession: windowState.selectedSession
        )
    }
}

/// Popover body: the same grouped Workspace chooser the old Menu offered,
/// with the filter field the pill design calls for.
private struct WorkspacePicker: View {
    @Environment(AppModel.self) private var appModel
    @Environment(WindowState.self) private var windowState
    @Environment(\.dismiss) private var dismiss

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

private extension View {
    /// Shared hover chrome for each pill region: its own subtle rounded
    /// highlight while the two regions still read as one continuous path.
    func regionChrome(isHighlighted: Bool) -> some View {
        padding(.horizontal, Spacing.sm)
            .padding(.vertical, 2)
            .background(
                .quaternary.opacity(isHighlighted ? 1 : 0),
                in: RoundedRectangle(cornerRadius: 5)
            )
            .contentShape(RoundedRectangle(cornerRadius: 5))
    }
}

struct WorkspaceSwitcherPresentation: Equatable {
    /// nil when no Active Workspace is selected — the pill then shows
    /// only `workspaceName` (the placeholder prompt).
    let projectName: String?
    let workspaceName: String
    let session: SessionIdentity?
    let workspaceHelp: String

    /// Flat "project › workspace" string for accessibility.
    var label: String {
        projectName.map { "\($0) › \(workspaceName)" } ?? workspaceName
    }

    static let noActiveWorkspace = WorkspaceSwitcherPresentation(
        projectName: nil,
        workspaceName: "Select Workspace…",
        session: nil,
        workspaceHelp: "Select an Active Workspace"
    )

    var help: String {
        session.map { "\(workspaceHelp) › \($0.indexedLabel)" } ?? workspaceHelp
    }

    private init(
        projectName: String?,
        workspaceName: String,
        session: SessionIdentity?,
        workspaceHelp: String
    ) {
        self.projectName = projectName
        self.workspaceName = workspaceName
        self.session = session
        self.workspaceHelp = workspaceHelp
    }

    init(project: Project, workspace: Workspace, session: Session? = nil) {
        projectName = project.name
        workspaceName = workspace.name
        self.session = session.map(SessionIdentity.init(session:))
        workspaceHelp = "\(project.name) › \(workspace.name)"
    }
}

import SwiftUI
import ATCAPI

/// Workspace-scoped Sessions and Terminals for the stable window sidebar.
/// Its data always derives from `WindowState.activeWorkspace`.
struct WorkspaceNavigatorView: View {
    @Environment(AppModel.self) private var appModel
    @Environment(WindowState.self) private var windowState

    @State private var actionError: String?
    @State private var deletingSession: WorkspaceSessionGroups.Row?
    @State private var renameRequest: SessionRenameRequest?

    var body: some View {
        if workspaceRef == nil {
            ContentUnavailableView(
                "No Active Workspace",
                systemImage: "rectangle.stack",
                description: Text("Open a workspace to see its sessions and terminals.")
            )
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        } else {
            navigatorContent
        }
    }

    @ViewBuilder
    private var navigatorContent: some View {
        @Bindable var windowState = windowState
        let groups = sessionGroups()

        NavigatorList {
            if runtime?.reachability == .unreachable {
                Label("Disconnected", systemImage: "cable.connector.slash")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .navigatorListRow()
            }

            NavigatorDisclosureHeader(
                title: "Sessions",
                isExpanded: $windowState.isSessionsSectionExpanded,
                addHelp: "New session",
                isAddEnabled: canStartSession,
                onAdd: { windowState.startSessionKind = .agentSession }
            )
            if windowState.isSessionsSectionExpanded {
                sessionRows(groups.sessions, emptyText: "No sessions")
            }

            NavigatorDisclosureHeader(
                title: "Terminals",
                isExpanded: $windowState.isTerminalsSectionExpanded,
                addHelp: "New terminal",
                isAddEnabled: canStartSession,
                onAdd: { windowState.startSessionKind = .terminal }
            )
            if windowState.isTerminalsSectionExpanded {
                sessionRows(groups.terminals, emptyText: "No terminals")
            }
        }
        .confirmationDialog(
            "Delete Session “\(deletingSession?.identity.indexedLabel ?? "")”?",
            isPresented: Binding(
                get: { deletingSession != nil },
                set: { if !$0 { deletingSession = nil } }
            )
        ) {
            Button("Delete Session", role: .destructive) {
                if let row = deletingSession { deleteSession(row) }
            }
            .disabled(!isConnected)
        } message: {
            if let row = deletingSession {
                Text(DeleteConfirmation.sessionMessage(
                    displayName: row.identity.indexedLabel,
                    status: row.session.status
                ))
            }
        }
        .alert(renameRequest?.dialogTitle ?? "Rename Session", isPresented: Binding(
            get: { renameRequest != nil },
            set: { if !$0 { renameRequest = nil } }
        )) {
            TextField("Name", text: renameDraft)
            Button("Rename") {
                renameSession()
            }
            .disabled(!(renameRequest?.canSubmit ?? false) || !isConnected)
            Button("Cancel", role: .cancel) {}
        }
        .actionErrorAlert($actionError, title: "Workspace Action Failed")
    }

    @ViewBuilder
    private func sessionRows(
        _ rows: [WorkspaceSessionGroups.Row],
        emptyText: String
    ) -> some View {
        ForEach(rows) { row in
            NavigatorRow(
                isSelected: windowState.selectedSession == row.ref,
                action: { _ = windowState.selectSession(row.ref, in: appModel) }
            ) { _ in
                HStack(spacing: Spacing.xs) {
                    if let index = row.identity.index {
                        SessionIndexBadge(index)
                    }
                    Text(row.identity.fullLabel)
                        .lineLimit(1)
                    if row.session.status == .ended {
                        StatusDot(color: .red)
                            .accessibilityLabel("Ended")
                            .help("Ended")
                    }
                }
            } actions: {
                EmptyView()
            }
            .accessibilityElement(children: .combine)
            .accessibilityLabel(
                row.session.status == .ended
                    ? "\(row.identity.accessibilityLabel), Ended"
                    : row.identity.accessibilityLabel
            )
            .help(row.identity.indexedLabel)
            .contextMenu { sessionMenu(row) }
        }
        if rows.isEmpty {
            Text(emptyText)
                .font(.caption)
                .foregroundStyle(.tertiary)
                .navigatorListRow()
        }
    }

    @ViewBuilder
    private func sessionMenu(_ row: WorkspaceSessionGroups.Row) -> some View {
        if row.session.status == .live {
            Button("Rename…", systemImage: "pencil") {
                renameRequest = SessionRenameRequest(
                    ref: row.ref,
                    identity: row.identity,
                    kind: row.kind
                )
            }
            .disabled(!isConnected)
            Divider()
        }
        Button("Delete…", systemImage: "trash", role: .destructive) {
            deletingSession = row
        }
        .disabled(!isConnected)
    }

    private var workspaceRef: WorkspaceRef? { windowState.activeWorkspace }

    private var runtime: ConnectionRuntime? {
        workspaceRef.flatMap { appModel.runtime(id: $0.connectionID) }
    }

    private var canStartSession: Bool {
        windowState.canStartSession(in: appModel)
    }

    private var isConnected: Bool {
        runtime?.reachability == .connected
    }

    private func workspaceSessions() -> [Session] {
        guard let ref = workspaceRef, let runtime else { return [] }
        return runtime.sessions.sessions.filter { $0.belongs(to: ref) }
    }

    private func sessionGroups() -> WorkspaceSessionGroups {
        guard let ref = workspaceRef else { return .empty }
        return WorkspaceSessionGroups(workspace: ref, sessions: workspaceSessions())
    }

    private func deleteSession(_ row: WorkspaceSessionGroups.Row) {
        appModel.deleteSession(ref: row.ref, windowState: windowState, reporting: $actionError)
    }

    private var renameDraft: Binding<String> {
        Binding(
            get: { renameRequest?.draft ?? "" },
            set: { renameRequest?.draft = $0 }
        )
    }

    private func renameSession() {
        guard let request = renameRequest,
              let runtime = appModel.runtime(id: request.ref.connectionID)
        else { return }
        let store = runtime.sessions
        appModel.run(on: runtime.id, reporting: $actionError) {
            try await store.rename(id: request.ref.sessionID, name: request.normalizedName)
        }
    }
}

struct SessionRenameRequest: Equatable {
    let ref: SessionRef
    let kind: SessionKind
    let identity: SessionIdentity
    private let originalName: String?
    var draft: String

    init(ref: SessionRef, identity: SessionIdentity, kind: SessionKind) {
        self.ref = ref
        self.kind = kind
        self.identity = identity
        originalName = identity.customName
        draft = identity.customName ?? ""
    }

    var dialogTitle: String {
        let category = kind == .agent ? "Session" : "Terminal"
        return "Rename \(category) “\(identity.indexedLabel)”"
    }

    var normalizedName: String? {
        let trimmed = draft.trimmingCharacters(in: .whitespacesAndNewlines)
        return trimmed.isEmpty ? nil : trimmed
    }

    var canSubmit: Bool {
        normalizedName != originalName
    }
}

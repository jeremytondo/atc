import SwiftUI
import Observation
import ATCAPI

enum NavigatorID: String, CaseIterable, Sendable {
    case projects
    case workspace
    case file

    var selectorLabel: String {
        switch self {
        case .projects: "Projects"
        case .workspace: "Workspace"
        case .file: "Files"
        }
    }

    var label: String {
        switch self {
        case .projects: "Projects Navigator"
        case .workspace: "Workspace Navigator"
        case .file: "File Navigator"
        }
    }

    var systemImage: String {
        switch self {
        case .projects: "folder"
        case .workspace: "rectangle.stack"
        case .file: "doc.text"
        }
    }
}

enum MainContentSelection: Equatable, Sendable {
    case dashboard
    case workspace(WorkspaceRef)
    case session(SessionRef)
}

struct TerminalRetentionContext: Equatable, Sendable {
    let activeWorkspace: WorkspaceRef?
    let selectedSession: SessionRef?

    static let empty = TerminalRetentionContext(activeWorkspace: nil, selectedSession: nil)
}

/// Per-window navigation, inspector, disclosure, and command state. The
/// AppModel continues to own shared data and terminal controllers; it does
/// not duplicate these identities.
@Observable
final class WindowState {
    var selectedNavigator: NavigatorID = .projects
    private(set) var activeWorkspace: WorkspaceRef?
    private(set) var selectedContent: MainContentSelection = .dashboard
    var columnVisibility: NavigationSplitViewVisibility = .all
    var isInspectorPresented = false

    var isProjectsSectionExpanded = true
    var isSessionsSectionExpanded = true
    var isTerminalsSectionExpanded = true
    var expandedProjects: Set<ProjectRef> = []

    var isCreateProjectPresented = false
    var createWorkspaceContext: CreateWorkspaceContext?
    var startSessionKind: StartSessionKind?

    @ObservationIgnored private let selectionMemory: WorkspaceSelectionMemory
    @ObservationIgnored private var pendingRestore: WorkspaceRef?

    init(selectionMemory: WorkspaceSelectionMemory = WorkspaceSelectionMemory()) {
        self.selectionMemory = selectionMemory
    }

    var selectedSession: SessionRef? {
        guard case .session(let ref) = selectedContent else { return nil }
        return ref
    }

    var retentionContext: TerminalRetentionContext {
        TerminalRetentionContext(
            activeWorkspace: activeWorkspace,
            selectedSession: selectedSession
        )
    }

    func hasInspectorTarget(in appModel: AppModel) -> Bool {
        guard case .session(let ref) = selectedContent else { return false }
        return appModel.session(for: ref) != nil && appModel.runtime(id: ref.connectionID) != nil
    }

    /// The single Workspace activation transition used by every entry point.
    /// A missing target is rejected without changing the current window.
    @discardableResult
    func activateWorkspace(_ ref: WorkspaceRef, in appModel: AppModel) -> Bool {
        guard let runtime = appModel.runtime(id: ref.connectionID),
              let workspace = runtime.workspaces.workspace(id: ref.workspaceID)
        else { return false }

        expandedProjects.insert(ProjectRef(
            connectionID: ref.connectionID,
            projectID: workspace.projectId
        ))

        // Re-selecting the current Workspace is normally idempotent, but from
        // Dashboard it is also the expected way back into that Workspace.
        if activeWorkspace == ref, selectedContent != .dashboard { return true }

        let sessionsCurrent = runtime.sessions.hasLoadedOnce && runtime.sessions.lastError == nil
        let restored = sessionsCurrent
            ? validRememberedSelection(for: ref, in: runtime.sessions.sessions)
            : nil

        activeWorkspace = ref
        selectedContent = restored.map(MainContentSelection.session) ?? .workspace(ref)
        isInspectorPresented = false
        pendingRestore = sessionsCurrent ? nil : ref

        if let restored, let session = appModel.session(for: restored), session.attachable {
            appModel.attachIfNeeded(
                to: session,
                connectionID: restored.connectionID,
                retentionContext: retentionContext
            )
        }
        return true
    }

    /// Selects content only when it belongs to the Active Workspace on the
    /// same Connection. Invalid cross-Workspace references fail closed.
    @discardableResult
    func selectSession(_ ref: SessionRef, in appModel: AppModel) -> Bool {
        guard let activeWorkspace,
              activeWorkspace.connectionID == ref.connectionID,
              let session = appModel.session(for: ref),
              session.belongs(to: activeWorkspace)
        else { return false }

        selectedContent = .session(ref)
        if session.isArchived {
            selectionMemory.forget(activeWorkspace)
        } else {
            selectionMemory.remember(sessionID: ref.sessionID, for: activeWorkspace)
        }
        if session.attachable {
            appModel.attachIfNeeded(
                to: session,
                connectionID: ref.connectionID,
                retentionContext: retentionContext
            )
        } else {
            appModel.touchTerminal(ref)
        }
        return true
    }

    func showDashboard() {
        selectedContent = .dashboard
        isInspectorPresented = false
    }

    func showWorkspaceEmpty() {
        guard let activeWorkspace else { return }
        selectionMemory.forget(activeWorkspace)
        selectedContent = .workspace(activeWorkspace)
        isInspectorPresented = false
    }

    /// Reconciles store-driven removal and delayed restoration. An unloaded
    /// store is unresolved and never clears the current Workspace.
    func reconcile(in appModel: AppModel) {
        guard let activeWorkspace else { return }
        guard let runtime = appModel.runtime(id: activeWorkspace.connectionID) else {
            selectionMemory.forget(connectionID: activeWorkspace.connectionID)
            handleActiveWorkspaceGone(activeWorkspace)
            return
        }
        guard runtime.workspaces.hasLoadedOnce, runtime.workspaces.lastError == nil else { return }
        guard runtime.workspaces.workspace(id: activeWorkspace.workspaceID) != nil else {
            handleActiveWorkspaceGone(activeWorkspace)
            return
        }

        if pendingRestore == activeWorkspace,
           runtime.sessions.hasLoadedOnce,
           runtime.sessions.lastError == nil {
            pendingRestore = nil
            if selectedContent == .workspace(activeWorkspace),
               let restored = validRememberedSelection(
                    for: activeWorkspace,
                    in: runtime.sessions.sessions
               ) {
                selectedContent = .session(restored)
                if let session = appModel.session(for: restored), session.attachable {
                    appModel.attachIfNeeded(
                        to: session,
                        connectionID: restored.connectionID,
                        retentionContext: retentionContext
                    )
                }
            }
        }

        guard case .session(let selected) = selectedContent,
              runtime.sessions.hasLoadedOnce,
              runtime.sessions.lastError == nil
        else { return }
        let session = appModel.session(for: selected)
        if selected.connectionID != activeWorkspace.connectionID
            || session?.belongs(to: activeWorkspace) != true {
            selectionMemory.forget(activeWorkspace)
            selectedContent = .workspace(activeWorkspace)
            isInspectorPresented = false
        } else if let session {
            if session.isArchived {
                selectionMemory.forget(activeWorkspace)
            }
            // Never undo an explicit Disconnect: reconcile runs on every
            // store change, not just selection changes.
            if session.attachable, appModel.terminals[selected] == nil,
               !appModel.isDetached(selected) {
                appModel.attachIfNeeded(
                    to: session,
                    connectionID: selected.connectionID,
                    retentionContext: retentionContext
                )
            }
        }
    }

    func forgetSelection(for ref: WorkspaceRef) {
        selectionMemory.forget(ref)
    }

    func toggleSidebar() {
        columnVisibility = columnVisibility == .detailOnly ? .all : .detailOnly
    }

    func canStartSession(in appModel: AppModel) -> Bool {
        activeWorkspace.map { appModel.canStartSession(in: $0) } ?? false
    }

    func presentCreateWorkspace(in appModel: AppModel) {
        if let activeWorkspace,
           let runtime = appModel.runtime(id: activeWorkspace.connectionID),
           let workspace = runtime.workspaces.workspace(id: activeWorkspace.workspaceID),
           let project = runtime.projects.project(id: workspace.projectId) {
            let projectRef = ProjectRef(
                connectionID: activeWorkspace.connectionID,
                projectID: project.id
            )
            guard appModel.canCreateWorkspace(in: projectRef) else {
                createWorkspaceContext = CreateWorkspaceContext(mode: .free)
                return
            }
            createWorkspaceContext = CreateWorkspaceContext(mode: .preselected(
                projectRef
            ))
        } else {
            createWorkspaceContext = CreateWorkspaceContext(mode: .free)
        }
    }

    private func validRememberedSelection(
        for ref: WorkspaceRef,
        in sessions: [Session]
    ) -> SessionRef? {
        let remembered = selectionMemory.sessionID(for: ref)
        let restored = selectionMemory.restoredSelection(for: ref, in: sessions)
        if remembered != nil, restored == nil {
            selectionMemory.forget(ref)
        }
        return restored
    }

    private func handleActiveWorkspaceGone(_ ref: WorkspaceRef) {
        selectionMemory.forget(ref)
        activeWorkspace = nil
        pendingRestore = nil
        selectedNavigator = .projects
        selectedContent = .dashboard
        isInspectorPresented = false
        // The start-session sheet renders from the Active Workspace; left
        // presented it would show an empty sheet with no way to cancel.
        startSessionKind = nil
    }
}

enum StartSessionKind: String, Identifiable {
    case agentSession
    case terminal

    var id: String { rawValue }
}

struct CreateWorkspaceContext: Identifiable, Hashable {
    enum Mode: Hashable {
        case fixed(ProjectRef)
        case preselected(ProjectRef)
        case free
    }

    let mode: Mode
    var id: CreateWorkspaceContext { self }
}

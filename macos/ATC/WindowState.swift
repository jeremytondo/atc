import SwiftUI
import Observation
import ATCAPI

/// The window's top-level surface: the Dashboard cover or the Workspace
/// shell beneath it.
enum Route {
    case dashboard
    case workspace
}

/// Per-window navigation and command state. Kept out of `AppModel` so a
/// future multi-window pass has one obvious seam; the app launches on the
/// Dashboard (the route is never persisted). Menu commands mutate this
/// object, `RootView` renders from it.
@Observable
final class WindowState {
    var route: Route = .dashboard

    /// Once a Workspace has been opened, the shell (and its terminal
    /// surfaces) stays mounted for the window's life; the Dashboard is an
    /// opaque cover over it, never a teardown.
    private(set) var hasOpenedWorkspaceShell = false

    var columnVisibility: NavigationSplitViewVisibility = .all

    // MARK: - Sheet routing (owned here so menu commands can present)

    var isCreateProjectPresented = false
    var createWorkspaceContext: CreateWorkspaceContext?
    var startSessionKind: StartSessionKind?

    /// Opens a Workspace in the shell: records it on the AppModel (which
    /// pins its sessions in the attachment budget) and routes the window.
    func openWorkspace(_ ref: WorkspaceRef, in appModel: AppModel) {
        appModel.openWorkspace = ref
        hasOpenedWorkspaceShell = true
        route = .workspace
    }

    func showDashboard() {
        route = .dashboard
    }

    /// The open Workspace vanished (deleted remotely, or its Connection
    /// was removed): back to the Dashboard.
    func handleOpenWorkspaceGone(in appModel: AppModel) {
        appModel.openWorkspace = nil
        route = .dashboard
    }

    func toggleSidebar() {
        columnVisibility = columnVisibility == .detailOnly ? .all : .detailOnly
    }

    // MARK: - Command availability

    /// `session.new` / `terminal.new` need an active open Workspace: the
    /// shell is the visible route, the Workspace is unarchived, and its
    /// Connection is reachable.
    func canStartSession(in appModel: AppModel) -> Bool {
        guard route == .workspace,
              let ref = appModel.openWorkspace,
              let runtime = appModel.runtime(id: ref.connectionID),
              runtime.reachability == .connected,
              let workspace = runtime.workspaces.workspace(id: ref.workspaceID),
              !workspace.isArchived
        else { return false }
        return true
    }

    /// `workspace.new` works everywhere: preselects the open Workspace's
    /// Project (changeable) when the shell is visible, else the
    /// context-free form with a required Project picker.
    func presentCreateWorkspace(in appModel: AppModel) {
        if route == .workspace,
           let ref = appModel.openWorkspace,
           let runtime = appModel.runtime(id: ref.connectionID),
           let workspace = runtime.workspaces.workspace(id: ref.workspaceID) {
            createWorkspaceContext = CreateWorkspaceContext(mode: .preselected(
                ProjectRef(connectionID: ref.connectionID, projectID: workspace.projectId)
            ))
        } else {
            createWorkspaceContext = CreateWorkspaceContext(mode: .free)
        }
    }
}

/// Which creation sheet `startSessionKind` presents.
enum StartSessionKind: String, Identifiable {
    /// Agent Actions only — the sidebar's Sessions section.
    case agentSession
    /// Interactive Shell or a general Action — the Terminals section.
    case terminal

    var id: String { rawValue }
}

/// Where the create-Workspace sheet was invoked from; decides how the
/// Project field behaves (see the spec's three contexts).
struct CreateWorkspaceContext: Identifiable, Hashable {
    enum Mode: Hashable {
        /// Project card / row button: preselected and fixed.
        case fixed(ProjectRef)
        /// `workspace.new` inside an open Workspace: preselected, changeable.
        case preselected(ProjectRef)
        /// File menu: required picker over unarchived Projects.
        case free
    }

    let mode: Mode
    var id: CreateWorkspaceContext { self }
}

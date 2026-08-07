import ATCAppServerAPI
import SwiftUI
import Observation

enum MainContentSelection: Equatable, Sendable {
    case dashboard
    case thread(ThreadRef)
    case terminal(TerminalRef)
}

/// The sidebar's thread filter. Resets to `.all` on every app launch; it
/// only affects the thread list beneath it, never pins, Dashboard, the
/// Terminals section, or New Thread defaults.
enum ThreadFilter: Equatable, Sendable {
    case all
    case project(ProjectRef)
    case archived
}

enum CommandPalettePresentation: Equatable, Sendable {
    case all
    case threads
    case terminals
}

struct TerminalRetentionContext: Equatable, Sendable {
    let visibleTerminal: TerminalRef?

    static let empty = TerminalRetentionContext(visibleTerminal: nil)
}

/// Everything a New Thread sheet needs to open with the right defaults.
struct NewThreadContext: Identifiable, Hashable {
    /// Pre-selected project; nil lets the sheet offer a picker.
    let projectRef: ProjectRef?
    var id: Self { self }
}

/// Per-window navigation, disclosure, sheet, and command state. The
/// AppModel continues to own shared data and terminal controllers; it does
/// not duplicate these identities.
///
/// Launch behavior per the design: open on Dashboard, filter reset to All
/// Projects, no restored selection, no Project context until a thread is
/// opened or created. Nothing here persists.
@Observable
final class WindowState {
    private(set) var selectedContent: MainContentSelection = .dashboard
    /// Launch-local Project context, established by opening or creating a
    /// Thread. Navigating to Dashboard does not clear it.
    private(set) var activeProject: ProjectRef?
    var threadFilter: ThreadFilter = .all
    var columnVisibility: NavigationSplitViewVisibility = .all
    var isInspectorPresented = false
    var commandPalettePresentation: CommandPalettePresentation?

    /// More/Hide expansion, preserved only for the current app launch.
    var isPinnedExpanded = false
    var isRecentExpanded = false
    var isArchivedExpanded = false
    var isTerminalsSectionExpanded = true

    var isCreateProjectPresented = false
    var newThreadContext: NewThreadContext?
    var newTerminalProject: ProjectRef?

    /// The TUI terminal each opened thread is currently displayed through.
    /// `openThread` records the server's answer here; store refreshes
    /// reconcile it (the thread's `linkedTerminalId` is the durable truth).
    private(set) var threadTerminals: [ThreadRef: TerminalRef] = [:]

    /// Threads with an open+attach in flight; re-selection is idempotent
    /// but a second concurrent open would hit the server's one-open guard.
    private(set) var openingThreads: Set<ThreadRef> = []

    /// Most recent failed open per thread, cleared on the next attempt.
    private(set) var threadOpenErrors: [ThreadRef: String] = [:]

    var isSheetPresented: Bool {
        isCreateProjectPresented || newThreadContext != nil || newTerminalProject != nil
    }

    /// Advances for every explicit request to type in the visible terminal.
    /// Unlike selection, this also changes when the user re-selects the
    /// same sidebar row.
    private(set) var terminalFocusRequest: UInt = 0

    var selectedThread: ThreadRef? {
        guard case .thread(let ref) = selectedContent else { return nil }
        return ref
    }

    /// The last opened thread: the sidebar's return-navigation anchor. While
    /// a standalone Terminal is displayed this thread keeps the stronger
    /// selection treatment (there is deliberately no toolbar back control);
    /// Dashboard clears the visible treatment but not the context.
    private(set) var returnThread: ThreadRef?

    /// Whether this thread's card should carry the strong selection
    /// treatment right now.
    func isThreadHighlighted(_ ref: ThreadRef) -> Bool {
        switch selectedContent {
        case .thread(let selected): selected == ref
        case .terminal: returnThread == ref
        case .dashboard: false
        }
    }

    /// The terminal the detail column should render right now.
    var visibleTerminal: TerminalRef? {
        switch selectedContent {
        case .dashboard: nil
        case .thread(let ref): threadTerminals[ref]
        case .terminal(let ref): ref
        }
    }

    var retentionContext: TerminalRetentionContext {
        TerminalRetentionContext(visibleTerminal: visibleTerminal)
    }

    // MARK: - Navigation transitions

    /// The single thread-open transition used by every entry point: select
    /// immediately (the content area shows the connecting state), then
    /// idempotently open the TUI terminal and attach. Establishes the
    /// thread's Project as the launch-local context.
    func openThread(_ ref: ThreadRef, in appModel: AppModel) async {
        guard let thread = appModel.thread(for: ref) else { return }
        guard !thread.isArchived else { return }

        selectedContent = .thread(ref)
        returnThread = ref
        activeProject = ProjectRef(connectionID: ref.connectionID, projectID: thread.projectId)
        threadOpenErrors[ref] = nil
        requestTerminalFocus()

        guard !openingThreads.contains(ref) else { return }
        openingThreads.insert(ref)
        defer { openingThreads.remove(ref) }
        do {
            let terminalRef = try await appModel.openThread(ref, retentionContext: retentionContext)
            threadTerminals[ref] = terminalRef
            requestTerminalFocus()
        } catch {
            threadOpenErrors[ref] = error.localizedDescription
        }
    }

    /// Selects a standalone Terminal. Live terminals attach; an ended one
    /// renders as a tombstone.
    func selectTerminal(_ ref: TerminalRef, in appModel: AppModel) {
        guard let terminal = appModel.terminal(for: ref) else { return }
        selectedContent = .terminal(ref)
        if terminal.isLive {
            appModel.attachIfNeeded(
                to: terminal,
                connectionID: ref.connectionID,
                retentionContext: retentionContext
            )
        } else {
            appModel.touchTerminal(ref)
        }
        requestTerminalFocus()
    }

    func showDashboard() {
        selectedContent = .dashboard
        isInspectorPresented = false
    }

    /// Reasserts terminal focus after transient UI (notably a creation
    /// sheet) has finished handing first-responder ownership back.
    func requestTerminalFocus() {
        guard visibleTerminal != nil else { return }
        terminalFocusRequest &+= 1
    }

    func toggleSidebar() {
        columnVisibility = columnVisibility == .detailOnly ? .all : .detailOnly
    }

    // MARK: - Sheet presentation

    /// New Thread pre-selects the launch-local context Project when the
    /// filter isn't pinning a different one.
    func presentNewThread(in appModel: AppModel) {
        if case .project(let ref) = threadFilter {
            newThreadContext = NewThreadContext(projectRef: ref)
            return
        }
        newThreadContext = NewThreadContext(projectRef: activeProject)
    }

    /// Called by the New Thread sheet after the server confirms creation:
    /// establishes context and opens the thread immediately.
    func threadCreated(_ ref: ThreadRef, in appModel: AppModel) {
        newThreadContext = nil
        Task { await openThread(ref, in: appModel) }
    }

    // MARK: - Reconciliation

    /// Reconciles store-driven removal: archived/deleted threads leave the
    /// content area, dead references reset to Dashboard, and the filter
    /// falls back to All Projects when its project disappears. An unloaded
    /// or failed store is unresolved and never clears state — connection
    /// loss must not look like deletion.
    func reconcile(in appModel: AppModel) {
        if case .project(let ref) = threadFilter {
            if let runtime = appModel.runtime(id: ref.connectionID) {
                if runtime.projects.isResolved,
                   runtime.projects.project(id: ref.projectID) == nil {
                    threadFilter = .all
                }
            } else {
                threadFilter = .all
            }
        }

        if let activeProject {
            if let runtime = appModel.runtime(id: activeProject.connectionID) {
                if runtime.projects.isResolved,
                   runtime.projects.project(id: activeProject.projectID) == nil {
                    self.activeProject = nil
                }
            } else {
                self.activeProject = nil
            }
        }

        // Prune mappings for threads that are gone (their Connection removed,
        // or the thread deleted per a resolved store); unresolved stores
        // leave entries alone.
        threadTerminals = threadTerminals.filter { ref, _ in
            guard let runtime = appModel.runtime(id: ref.connectionID) else { return false }
            guard runtime.threads.isResolved else { return true }
            return runtime.threads.thread(id: ref.threadID) != nil
        }

        if let ref = returnThread {
            if let runtime = appModel.runtime(id: ref.connectionID) {
                if runtime.threads.isResolved,
                   appModel.thread(for: ref)?.isArchived != false {
                    returnThread = nil
                }
            } else {
                returnThread = nil
            }
        }

        switch selectedContent {
        case .dashboard:
            break
        case .thread(let ref):
            guard let runtime = appModel.runtime(id: ref.connectionID) else {
                showDashboard()
                return
            }
            guard runtime.threads.isResolved else { return }
            guard let thread = runtime.threads.thread(id: ref.threadID), !thread.isArchived else {
                // Archiving or deleting the displayed thread returns to
                // Dashboard; its Project stays as launch-local context.
                threadTerminals[ref] = nil
                showDashboard()
                return
            }
            reconcileThreadTerminal(ref, thread: thread, in: appModel)
        case .terminal(let ref):
            guard let runtime = appModel.runtime(id: ref.connectionID) else {
                showDashboard()
                return
            }
            guard runtime.terminals.isResolved else { return }
            guard let terminal = appModel.terminal(for: ref) else {
                showDashboard()
                return
            }
            // A live displayed terminal that lost its controller (most
            // commonly a Connection rebuild) reattaches automatically, the
            // same rule the thread path applies.
            if terminal.isLive, appModel.terminals[ref] == nil {
                appModel.attachIfNeeded(
                    to: terminal,
                    connectionID: ref.connectionID,
                    retentionContext: retentionContext
                )
            }
        }
    }

    /// Keeps the displayed thread's terminal mapping and attach current:
    /// adopt the server's linked terminal when it differs (a relaunch made
    /// a new one), and reattach a live terminal that lost its controller.
    private func reconcileThreadTerminal(_ ref: ThreadRef, thread: ATCThread, in appModel: AppModel) {
        if let linkedID = thread.linkedTerminalId {
            let linkedRef = TerminalRef(connectionID: ref.connectionID, terminalID: linkedID)
            threadTerminals[ref] = linkedRef
            if let terminal = appModel.terminal(for: linkedRef), terminal.isLive,
               appModel.terminals[linkedRef] == nil {
                appModel.attachIfNeeded(
                    to: terminal,
                    connectionID: ref.connectionID,
                    retentionContext: retentionContext
                )
            }
            return
        }
        // No live linked terminal server-side. Keep an ended controller's
        // final frame (the relaunch affordance renders over it); drop the
        // mapping only when nothing is retained for it anymore.
        if let terminalRef = threadTerminals[ref], appModel.terminals[terminalRef] == nil {
            threadTerminals[ref] = nil
        }
    }
}

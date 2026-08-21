import ATCAppServerAPI
import AppKit
import Observation
import SwiftUI

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

/// What a New Thread composer opens with (ATC-216): the project in
/// context, if any — the draft's chips fill in the rest.
struct NewThreadContext: Hashable {
    /// Pre-selected project; nil keeps the draft's own (or the first offered).
    let projectRef: ProjectRef?
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
    /// Bumped when a selection made outside the sidebar (palette, ⌘1–9,
    /// content-area links) should scroll the sidebar to its row. Sidebar
    /// clicks pass `reveal: false`, so clicking a visible row never yanks
    /// it out from under the pointer.
    private(set) var sidebarRevealToken = 0
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
    /// The New Thread composer is shown over the content area while set
    /// (not a sheet: the sidebar stays live, and navigating anywhere
    /// dismisses it — the draft survives on `AppModel`).
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

    /// Threads whose TUI open was refused because the server is driving a
    /// turn (ATC-203): the server launches the TUI when the turn ends and
    /// reconciliation attaches it. Cleared when the linked terminal appears
    /// or an open succeeds.
    private(set) var threadsAwaitingTui: Set<ThreadRef> = []

    /// Whether the app is frontmost — the seam tests override; production
    /// asks AppKit. Viewing only counts when the user can actually see it.
    @ObservationIgnored var isAppActive: () -> Bool = { AppActivity.isActive() }

    /// Mark-viewed requests in flight, so a burst of reconciliations does
    /// not re-stamp the same thread before the store merge lands.
    @ObservationIgnored private var markingViewed: Set<ThreadRef> = []

    var isSheetPresented: Bool {
        isCreateProjectPresented || newTerminalProject != nil
    }

    /// Advances for every explicit request to type in the displayed content
    /// — the visible terminal, or the Chat composer. Unlike selection, this
    /// also changes when the user re-selects the same sidebar row. Only the
    /// surface actually on screen answers it.
    private(set) var contentFocusRequest: UInt = 0

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
    /// immediately, establish the thread's Project as the launch-local
    /// context, then — only in TUI mode — idempotently open the TUI terminal
    /// and attach. Chat mode never opens or attaches a terminal; the Chat
    /// view acquires the thread's transcript on display instead.
    func openThread(_ ref: ThreadRef, in appModel: AppModel, reveal: Bool = true) async {
        guard let thread = appModel.thread(for: ref) else { return }
        guard !thread.isArchived else { return }

        appModel.noteThreadOpened()
        newThreadContext = nil
        selectedContent = .thread(ref)
        if reveal { sidebarRevealToken += 1 }
        returnThread = ref
        activeProject = ProjectRef(connectionID: ref.connectionID, projectID: thread.projectId)
        threadOpenErrors[ref] = nil
        // Opening is viewing (ATC-160). Guarded on the store's unread flag;
        // a finish the store hasn't refreshed into yet is caught by
        // reconciliation once it lands (markViewedIfDisplayed).
        if thread.unread { markViewed(ref, in: appModel) }
        requestContentFocus()

        guard appModel.viewMode(for: ref) == .tui else { return }
        guard !openingThreads.contains(ref) else { return }
        openingThreads.insert(ref)
        defer { openingThreads.remove(ref) }
        do {
            let terminalRef = try await appModel.openThread(ref, retentionContext: retentionContext)
            threadTerminals[ref] = terminalRef
            threadsAwaitingTui.remove(ref)
            // The user may have flipped back to Chat while the open was in
            // flight; the terminal stays retained (hidden), Chat keeps the
            // focus, and the server hears that Chat is what is shown (the
            // open must not be the last word).
            if appModel.viewMode(for: ref) == .tui {
                requestContentFocus()
            } else {
                appModel.closeTerminalIfChat(ref)
            }
        } catch is ThreadTerminalBusy {
            // The server is driving a turn: it launches the TUI when the
            // turn ends, and reconciliation attaches it (the linked
            // terminal appears on the thread). Not an error to show — unless
            // the user flipped back to Chat meanwhile: then the request is
            // retracted (a close after the open, so the later one wins).
            guard appModel.viewMode(for: ref) == .tui else {
                threadsAwaitingTui.remove(ref)
                appModel.closeTerminalIfChat(ref)
                return
            }
            threadsAwaitingTui.insert(ref)
        } catch {
            threadOpenErrors[ref] = error.localizedDescription
        }
    }

    /// Flips the displayed thread between Chat and TUI. The mode is
    /// app-wide (`AppModel.setViewMode`), which re-opens the thread in every
    /// window showing it.
    func toggleViewMode(in appModel: AppModel) {
        guard let ref = selectedThread else { return }
        appModel.setViewMode(appModel.viewMode(for: ref) == .chat ? .tui : .chat, for: ref)
    }

    /// Selects a standalone Terminal. Live terminals attach; an ended one
    /// renders as a tombstone.
    func selectTerminal(_ ref: TerminalRef, in appModel: AppModel, reveal: Bool = true) {
        guard let terminal = appModel.terminal(for: ref) else { return }
        newThreadContext = nil
        selectedContent = .terminal(ref)
        if reveal { sidebarRevealToken += 1 }
        if terminal.isLive {
            appModel.attachIfNeeded(
                to: terminal,
                connectionID: ref.connectionID,
                retentionContext: retentionContext
            )
        } else {
            appModel.touchTerminal(ref)
        }
        requestContentFocus()
    }

    func showDashboard() {
        newThreadContext = nil
        selectedContent = .dashboard
        isInspectorPresented = false
    }

    /// Reasserts content focus after transient UI (notably a creation
    /// sheet) has finished handing first-responder ownership back.
    func requestContentFocus() {
        contentFocusRequest &+= 1
    }

    func toggleSidebar() {
        columnVisibility = columnVisibility == .detailOnly ? .all : .detailOnly
    }

    // MARK: - Sheet and composer presentation

    /// New Thread opens the composer with the launch-local context Project
    /// when the filter isn't pinning a different one.
    func presentNewThread() {
        if case .project(let ref) = threadFilter {
            newThreadContext = NewThreadContext(projectRef: ref)
            return
        }
        newThreadContext = NewThreadContext(projectRef: activeProject)
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
                    runtime.projects.project(id: ref.projectID) == nil
                {
                    threadFilter = .all
                }
            } else {
                threadFilter = .all
            }
        }

        if let activeProject {
            if let runtime = appModel.runtime(id: activeProject.connectionID) {
                if runtime.projects.isResolved,
                    runtime.projects.project(id: activeProject.projectID) == nil
                {
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
                    appModel.thread(for: ref)?.isArchived != false
                {
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
            markViewedIfDisplayed(ref, thread: thread, in: appModel)
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

    /// App-activation hook (RootView's scenePhase): a finish that landed
    /// while the app was backgrounded is on screen the moment the user
    /// returns, so the displayed thread must not keep reading "Done".
    func markSelectedThreadViewedIfNeeded(in appModel: AppModel) {
        guard case .thread(let ref) = selectedContent,
            let thread = appModel.thread(for: ref)
        else { return }
        markViewedIfDisplayed(ref, thread: thread, in: appModel)
    }

    /// A finish that lands while its thread is already displayed in the
    /// frontmost app was watched, not missed: stamp viewed immediately so
    /// no "Done" flashes for a result the user saw happen (ATC-160).
    private func markViewedIfDisplayed(_ ref: ThreadRef, thread: ATCThread, in appModel: AppModel) {
        guard thread.unread, isAppActive() else { return }
        markViewed(ref, in: appModel)
    }

    /// The one mark-viewed dispatch: deduplicates in-flight stamps. State
    /// changes only through the store's merge of the server response.
    private func markViewed(_ ref: ThreadRef, in appModel: AppModel) {
        guard !markingViewed.contains(ref) else { return }
        markingViewed.insert(ref)
        Task {
            await appModel.markThreadViewed(ref)
            markingViewed.remove(ref)
            // A finish that landed during the request re-armed unread while
            // the dedup guard was refusing a second stamp — re-check so the
            // displayed thread never sticks on "Done".
            markSelectedThreadViewedIfNeeded(in: appModel)
        }
    }

    /// Keeps the displayed thread's terminal mapping and attach current:
    /// adopt the server's linked terminal when it differs (a relaunch made
    /// a new one), and — in TUI mode — reattach a live terminal that lost
    /// its controller. Chat mode tracks the mapping but never attaches; the
    /// switch to TUI opens (and attaches) on its own.
    private func reconcileThreadTerminal(_ ref: ThreadRef, thread: ATCThread, in appModel: AppModel) {
        if let linkedID = thread.linkedTerminalId {
            let linkedRef = TerminalRef(connectionID: ref.connectionID, terminalID: linkedID)
            threadTerminals[ref] = linkedRef
            threadsAwaitingTui.remove(ref)
            if appModel.viewMode(for: ref) == .tui,
                let terminal = appModel.terminal(for: linkedRef), terminal.isLive,
                appModel.terminals[linkedRef] == nil
            {
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

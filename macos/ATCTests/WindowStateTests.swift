import ATCAppServerAPI
import Foundation
import SwiftUI
import Testing

@testable import ATC

/// Per-window navigation: the one thread-open transition, and the
/// reconciliation that must distinguish "the server says it is gone" from
/// "we cannot reach the server".
@MainActor
@Suite("WindowState")
struct WindowStateTests {
    private func loadedModel(
        _ client: ScriptableAppServerClient = ScriptableAppServerClient()
    ) async throws -> TestModel {
        Fixtures.seed(client)
        let test = try await makeModel(client: client)
        await test.runtime.refresh()
        await test.runtime.threads.loadArchivedIfNeeded()
        return test
    }

    /// The terminal-first path: these suites predate Chat, so the thread
    /// is put in TUI mode before opening.
    private func openInTUI(_ ref: ThreadRef, state: WindowState, in test: TestModel) async {
        test.model.setViewMode(.tui, for: ref)
        await state.openThread(ref, in: test.model)
    }

    // MARK: - Launch state

    @Test("a window opens on Dashboard with no project context and no filter")
    func launchState() {
        let state = WindowState()
        #expect(state.selectedContent == .dashboard)
        #expect(state.activeProject == nil)
        #expect(state.threadFilter == .all)
        #expect(state.visibleTerminal == nil)
        #expect(!state.isSheetPresented)
        #expect(state.commandPalettePresentation == nil)
    }

    // MARK: - Opening threads

    @Test("openThread in TUI selects the thread, adopts its project, and records its terminal")
    func openThreadSucceeds() async throws {
        let test = try await loadedModel()
        let state = WindowState()
        let ref = test.threadRef("thr1")

        await openInTUI(ref, state: state, in: test)

        #expect(state.selectedContent == .thread(ref))
        #expect(state.selectedThread == ref)
        #expect(state.activeProject == test.projectRef("prj"))
        let terminalRef = try #require(state.threadTerminals[ref])
        #expect(state.visibleTerminal == terminalRef)
        #expect(test.model.terminals[terminalRef] != nil)
        #expect(state.threadOpenErrors[ref] == nil)
        #expect(state.contentFocusRequest > 0)
    }

    @Test("a failed open keeps the selection and records the error")
    func openThreadFailureKeepsSelection() async throws {
        let client = ScriptableAppServerClient()
        let test = try await loadedModel(client)
        let state = WindowState()
        let ref = test.threadRef("thr1")

        client.shouldFail = true
        await openInTUI(ref, state: state, in: test)

        // The content area stays on the thread and explains itself; bouncing
        // back to Dashboard would lose the user's place on a transient error.
        #expect(state.selectedContent == .thread(ref))
        #expect(state.activeProject == test.projectRef("prj"))
        #expect(state.threadTerminals[ref] == nil)
        #expect(state.threadOpenErrors[ref] != nil)

        // The next attempt clears the stale error.
        client.shouldFail = false
        await state.openThread(ref, in: test.model)
        #expect(state.threadOpenErrors[ref] == nil)
        #expect(state.threadTerminals[ref] != nil)
    }

    @Test("an archived thread refuses to open and never moves the selection")
    func archivedThreadRefusesToOpen() async throws {
        let test = try await loadedModel()
        let state = WindowState()

        await state.openThread(test.threadRef("thr_archived"), in: test.model)
        #expect(state.selectedContent == .dashboard)
        #expect(state.activeProject == nil)
        #expect(state.threadTerminals.isEmpty)

        // A thread this window knows nothing about is equally inert.
        await state.openThread(test.threadRef("nope"), in: test.model)
        #expect(state.selectedContent == .dashboard)
    }

    @Test("selecting a live terminal attaches it; an ended one is only retained")
    func selectTerminal() async throws {
        let test = try await loadedModel()
        let state = WindowState()

        let live = test.terminalRef("trm_live")
        state.selectTerminal(live, in: test.model)
        #expect(state.selectedContent == .terminal(live))
        #expect(state.visibleTerminal == live)
        #expect(test.model.terminals[live] != nil)

        let ended = test.terminalRef("trm_ended")
        state.selectTerminal(ended, in: test.model)
        #expect(state.selectedContent == .terminal(ended))
        #expect(test.model.terminals[ended] == nil)
    }

    @Test("showDashboard keeps the launch-local project context")
    func showDashboardKeepsContext() async throws {
        let test = try await loadedModel()
        let state = WindowState()
        await state.openThread(test.threadRef("thr1"), in: test.model)

        state.showDashboard()
        #expect(state.selectedContent == .dashboard)
        #expect(state.activeProject == test.projectRef("prj"))
        #expect(!state.isInspectorPresented)
    }

    // MARK: - Sheets

    @Test("New Thread prefers the filter's project, else the launch-local context")
    func newThreadDefaults() async throws {
        let test = try await loadedModel()
        let state = WindowState()

        state.presentNewThread()
        #expect(try #require(state.newThreadContext).projectRef == nil)
        #expect(state.isSheetPresented)
        state.newThreadContext = nil

        await state.openThread(test.threadRef("thr1"), in: test.model)
        state.presentNewThread()
        #expect(try #require(state.newThreadContext).projectRef == test.projectRef("prj"))
        state.newThreadContext = nil

        // The filter wins over the launch-local context when it pins a
        // different Project.
        let pinned = ProjectRef(connectionID: test.connectionID, projectID: "other")
        state.threadFilter = .project(pinned)
        state.presentNewThread()
        #expect(try #require(state.newThreadContext).projectRef == pinned)
    }

    // MARK: - Reconciliation

    @Test("archiving the displayed thread returns to Dashboard but keeps its project")
    func archivingDisplayedThreadReturnsToDashboard() async throws {
        let client = ScriptableAppServerClient()
        let test = try await loadedModel(client)
        let state = WindowState()
        let ref = test.threadRef("thr1")
        await openInTUI(ref, state: state, in: test)

        try await test.runtime.threads.archive(id: "thr1")
        state.reconcile(in: test.model)

        #expect(state.selectedContent == .dashboard)
        #expect(state.threadTerminals[ref] == nil)
        // The Project stays as launch-local context.
        #expect(state.activeProject == test.projectRef("prj"))
    }

    @Test("deleting the displayed thread returns to Dashboard")
    func deletingDisplayedThreadReturnsToDashboard() async throws {
        let test = try await loadedModel()
        let state = WindowState()
        await state.openThread(test.threadRef("thr1"), in: test.model)

        try await test.runtime.threads.delete(id: "thr1")
        state.reconcile(in: test.model)
        #expect(state.selectedContent == .dashboard)
    }

    @Test("an unreachable connection never looks like a deletion")
    func failedRefreshDoesNotClearSelection() async throws {
        let client = ScriptableAppServerClient()
        let test = try await loadedModel(client)
        let state = WindowState()
        let ref = test.threadRef("thr1")
        await state.openThread(ref, in: test.model)
        state.threadFilter = .project(test.projectRef("prj"))

        client.threads = []
        client.projects = []
        client.shouldFail = true
        await test.runtime.refresh()
        state.reconcile(in: test.model)

        #expect(state.selectedContent == .thread(ref))
        #expect(state.threadFilter == .project(test.projectRef("prj")))
        #expect(state.activeProject == test.projectRef("prj"))
    }

    @Test("the filter and the project context fall back when their project disappears")
    func deletedProjectResetsFilter() async throws {
        let client = ScriptableAppServerClient()
        let test = try await loadedModel(client)
        let state = WindowState()
        await state.openThread(test.threadRef("thr1"), in: test.model)
        state.threadFilter = .project(test.projectRef("prj"))

        try await test.runtime.projects.delete(id: "prj")
        state.reconcile(in: test.model)

        #expect(state.threadFilter == .all)
        #expect(state.activeProject == nil)
    }

    @Test("removing the connection resets every reference to Dashboard")
    func connectionRemovalResetsSelection() async throws {
        let test = try await loadedModel()
        let state = WindowState()
        let ref = test.threadRef("thr1")
        await openInTUI(ref, state: state, in: test)
        state.threadFilter = .project(test.projectRef("prj"))

        test.model.removeConnection(id: test.connectionID)
        state.reconcile(in: test.model)

        #expect(state.selectedContent == .dashboard)
        #expect(state.threadFilter == .all)
        #expect(state.activeProject == nil)
        #expect(state.threadTerminals.isEmpty)
    }

    @Test("a selected terminal the server no longer lists returns to Dashboard")
    func deletedTerminalReturnsToDashboard() async throws {
        let test = try await loadedModel()
        let state = WindowState()
        state.selectTerminal(test.terminalRef("trm_live"), in: test.model)

        try await test.runtime.terminals.delete(id: "trm_live")
        state.reconcile(in: test.model)
        #expect(state.selectedContent == .dashboard)
    }

    @Test("reconcile adopts the server's linked terminal and reattaches it")
    func adoptsServerLinkedTerminal() async throws {
        let client = ScriptableAppServerClient()
        let test = try await loadedModel(client)
        let state = WindowState()
        let ref = test.threadRef("thr1")
        await openInTUI(ref, state: state, in: test)
        let original = try #require(state.threadTerminals[ref])

        // A relaunch elsewhere replaced the thread's TUI terminal.
        client.terminals.removeAll { $0.id == original.terminalID }
        let replacement = Fixtures.terminal(
            id: "trm_relaunched", threadId: "thr1", createdAt: Fixtures.date(100)
        )
        client.terminals.append(replacement)
        client.threads = client.threads.map { thread in
            guard thread.id == "thr1" else { return thread }
            var updated = thread
            updated.linkedTerminalId = replacement.id
            return updated
        }
        await test.runtime.refresh()
        state.reconcile(in: test.model)

        let adopted = test.terminalRef(replacement.id)
        #expect(state.threadTerminals[ref] == adopted)
        #expect(state.visibleTerminal == adopted)
        #expect(test.model.terminals[adopted] != nil)
    }

    @Test("a displayed thread's live terminal that lost its controller reattaches")
    func lostControllerIsReattached() async throws {
        let test = try await loadedModel()
        let state = WindowState()
        let ref = test.threadRef("thr1")
        await openInTUI(ref, state: state, in: test)
        let terminalRef = try #require(state.threadTerminals[ref])

        // Eviction or Connection teardown released the controller while the
        // terminal stayed live server-side.
        test.model.disconnectTerminal(ref: terminalRef)
        #expect(test.model.terminals[terminalRef] == nil)

        await test.runtime.refresh()
        state.reconcile(in: test.model)

        #expect(state.threadTerminals[ref] == terminalRef)
        #expect(test.model.terminals[terminalRef] != nil)
    }

    @Test("an unlinked-but-live terminal keeps its mapping while retained")
    func keepsMappingWhileRetained() async throws {
        let client = ScriptableAppServerClient()
        let test = try await loadedModel(client)
        let state = WindowState()
        let ref = test.threadRef("thr1")
        await openInTUI(ref, state: state, in: test)
        let terminalRef = try #require(state.threadTerminals[ref])

        // The thread lets go of its terminal but the terminal row stays
        // live: lifecycle reconciliation keeps the controller, and the
        // window keeps the mapping — the retained final frame.
        client.threads = client.threads.map { thread in
            guard thread.id == "thr1" else { return thread }
            var updated = thread
            updated.linkedTerminalId = nil
            return updated
        }
        await test.runtime.refresh()
        await settle(until: { test.model.thread(for: ref)?.linkedTerminalId == nil })
        state.reconcile(in: test.model)
        #expect(state.threadTerminals[ref] == terminalRef)
        #expect(test.model.terminals[terminalRef] != nil)
    }

    @Test("a thread whose server-side terminal vanishes drops its mapping")
    func dropsMappingWhenNothingRetained() async throws {
        let client = ScriptableAppServerClient()
        let test = try await loadedModel(client)
        let state = WindowState()
        let ref = test.threadRef("thr1")
        await openInTUI(ref, state: state, in: test)
        let terminalRef = try #require(state.threadTerminals[ref])

        client.terminals.removeAll { $0.id == terminalRef.terminalID }
        client.threads = client.threads.map { thread in
            guard thread.id == "thr1" else { return thread }
            var updated = thread
            updated.linkedTerminalId = nil
            return updated
        }
        await test.runtime.refresh()
        // The model observes its own snapshot now (ATC-168 M2): lifecycle
        // reconciliation drops the vanished row's controller without any
        // window involvement, and the window reconcile then drops the
        // mapping (nothing retained).
        await settle(until: { test.model.terminals[terminalRef] == nil })
        state.reconcile(in: test.model)
        #expect(state.threadTerminals[ref] == nil)
    }

    // MARK: - Unread (ATC-160)

    /// Marks one seeded thread unread server-side, as a finish would.
    private func setUnread(_ id: String, _ unread: Bool, on client: ScriptableAppServerClient) {
        client.threads = client.threads.map { thread in
            guard thread.id == id else { return thread }
            var updated = thread
            updated.unread = unread
            return updated
        }
    }

    @Test("opening an unread thread stamps it viewed")
    func openingStampsViewed() async throws {
        let client = ScriptableAppServerClient()
        Fixtures.seed(client)
        setUnread("thr1", true, on: client)
        let test = try await makeModel(client: client)
        await test.runtime.refresh()
        let state = WindowState()
        let ref = test.threadRef("thr1")

        await state.openThread(ref, in: test.model)
        // The stamp merges the server's answer back into the store.
        await settle(until: { test.model.thread(for: ref)?.unread == false })
        #expect(client.markThreadViewedCount == 1)
        // A read thread costs nothing: re-opening never stamps again.
        await state.openThread(ref, in: test.model)
        #expect(client.markThreadViewedCount == 1)
    }

    @Test("a finish landing under the displayed frontmost thread is stamped viewed")
    func finishWhileWatchingIsViewed() async throws {
        let client = ScriptableAppServerClient()
        let test = try await loadedModel(client)
        let state = WindowState()
        state.isAppActive = { true }
        let ref = test.threadRef("thr1")
        await state.openThread(ref, in: test.model)
        #expect(client.markThreadViewedCount == 0)

        // The turn finishes while the user is watching: the refresh lands
        // unread, and reconciliation stamps it before any "Done" flash.
        setUnread("thr1", true, on: client)
        await test.runtime.refresh()
        state.reconcile(in: test.model)
        await settle(until: { client.threads.first { $0.id == "thr1" }?.unread == false })
        #expect(client.markThreadViewedCount == 1)
    }

    @Test("a finish while the app is in the background stays unread")
    func backgroundFinishStaysUnread() async throws {
        let client = ScriptableAppServerClient()
        let test = try await loadedModel(client)
        let state = WindowState()
        state.isAppActive = { false }
        let ref = test.threadRef("thr1")
        await state.openThread(ref, in: test.model)

        setUnread("thr1", true, on: client)
        await test.runtime.refresh()
        state.reconcile(in: test.model)
        #expect(client.markThreadViewedCount == 0)
        #expect(test.model.thread(for: ref)?.unread == true)

        // Returning to the app with the result on screen counts as viewing
        // (the RootView scenePhase hook calls exactly this).
        state.isAppActive = { true }
        state.markSelectedThreadViewedIfNeeded(in: test.model)
        await settle(until: { client.threads.first { $0.id == "thr1" }?.unread == false })
        #expect(client.markThreadViewedCount == 1)
    }

    @Test("a finish on an unselected thread is never stamped")
    func unselectedThreadStaysUnread() async throws {
        let client = ScriptableAppServerClient()
        let test = try await loadedModel(client)
        let state = WindowState()
        state.isAppActive = { true }
        await state.openThread(test.threadRef("thr1"), in: test.model)

        setUnread("thr2", true, on: client)
        await test.runtime.refresh()
        state.reconcile(in: test.model)
        state.markSelectedThreadViewedIfNeeded(in: test.model)
        #expect(client.markThreadViewedCount == 0)
        #expect(test.model.thread(for: test.threadRef("thr2"))?.unread == true)
    }

    // MARK: - Chrome

    @Test("toggleSidebar flips between the full layout and detail-only")
    func toggleSidebar() {
        let state = WindowState()
        #expect(state.columnVisibility == .all)
        state.toggleSidebar()
        #expect(state.columnVisibility == .detailOnly)
        state.toggleSidebar()
        #expect(state.columnVisibility == .all)
    }

    @Test("content focus advances on every request, including re-selecting the same row")
    func focusRequestsAdvance() async throws {
        let test = try await loadedModel()
        let state = WindowState()
        state.selectTerminal(test.terminalRef("trm_live"), in: test.model)
        let first = state.contentFocusRequest
        #expect(first > 0)
        state.selectTerminal(test.terminalRef("trm_live"), in: test.model)
        #expect(state.contentFocusRequest == first + 1)
        state.requestContentFocus()
        #expect(state.contentFocusRequest == first + 2)
    }

    // MARK: - Chat and TUI

    @Test("Chat is the default: opening a thread selects it and never opens or attaches a terminal")
    func chatIsDefault() async throws {
        let test = try await loadedModel()
        let state = WindowState()
        let ref = test.threadRef("thr1")

        await state.openThread(ref, in: test.model)

        #expect(test.model.viewMode(for: ref) == .chat)
        #expect(state.selectedContent == .thread(ref))
        #expect(state.activeProject == test.projectRef("prj"))
        #expect(state.threadTerminals[ref] == nil)
        #expect(test.model.terminals.isEmpty)
        #expect(test.client.openThreadTerminalCount == 0)
        #expect(state.contentFocusRequest > 0)
    }

    @Test("switching the displayed thread to TUI opens its terminal; back to Chat leaves it retained")
    func switchingModes() async throws {
        let test = try await loadedModel()
        let state = WindowState()
        test.model.registerWindow(state)
        let ref = test.threadRef("thr1")
        await state.openThread(ref, in: test.model)

        test.model.setViewMode(.tui, for: ref)
        await settle(until: { state.threadTerminals[ref] != nil })
        let terminalRef = try #require(state.threadTerminals[ref])
        #expect(test.model.terminals[terminalRef] != nil)
        #expect(test.client.openThreadTerminalCount == 1)

        test.model.setViewMode(.chat, for: ref)
        await drainPendingTasks()
        // The surface survives the flip; only what the pane shows changes —
        // and the server is told the TUI is no longer shown (ATC-203), so
        // it can hand a one-process thread back to Chat.
        #expect(state.threadTerminals[ref] == terminalRef)
        #expect(test.model.terminals[terminalRef] != nil)
        #expect(test.client.openThreadTerminalCount == 1)
        await settle(until: { test.client.closeThreadTerminalCount == 1 })

        state.toggleViewMode(in: test.model)
        #expect(test.model.viewMode(for: ref) == .tui)
        await drainPendingTasks()
        #expect(test.client.closeThreadTerminalCount == 1)
    }

    @Test("composer drafts are per-thread and survive switching modes")
    func composerDraftsSurviveSwitchingModes() async throws {
        let test = try await loadedModel()
        let ref = test.threadRef("thr1")
        let other = test.threadRef("thr2")

        test.model.setDraft("unfinished prompt", for: ref)
        #expect(test.model.draft(for: ref) == "unfinished prompt")
        #expect(test.model.draft(for: other) == "")
        test.model.setViewMode(.tui, for: ref)
        test.model.setViewMode(.chat, for: ref)
        #expect(test.model.draft(for: ref) == "unfinished prompt")
        test.model.setDraft("", for: ref)
        #expect(test.model.threadDrafts[ref] == nil)
    }

    @Test("a TUI open refused while the server drives a turn waits, then attaches the terminal it launches")
    func deferredTuiOpenAttachesOnArrival() async throws {
        let client = ScriptableAppServerClient()
        let test = try await loadedModel(client)
        let state = WindowState()
        test.model.registerWindow(state)
        let ref = test.threadRef("thr1")
        client.openThreadTerminalBusy = true

        test.model.setViewMode(.tui, for: ref)
        await state.openThread(ref, in: test.model)
        await settle(until: { state.threadsAwaitingTui.contains(ref) })
        // A wait, not an error: no banner message, nothing attached.
        #expect(state.threadOpenErrors[ref] == nil)
        #expect(state.threadTerminals[ref] == nil)

        // The server's turn ends and it launches the TUI itself: the linked
        // terminal appears on the thread and reconciliation attaches it.
        client.openThreadTerminalBusy = false
        let linked = Fixtures.terminal(id: "trm_deferred", threadId: "thr1")
        client.terminals += [linked]
        client.threads = client.threads.map { thread in
            var thread = thread
            if thread.id == "thr1" { thread.linkedTerminalId = "trm_deferred" }
            return thread
        }
        await test.runtime.refresh()
        state.reconcile(in: test.model)

        #expect(state.threadTerminals[ref] == test.terminalRef("trm_deferred"))
        #expect(!state.threadsAwaitingTui.contains(ref))
        #expect(test.model.terminals[test.terminalRef("trm_deferred")] != nil)
    }

    @Test("the mode is remembered per thread and shared by every window showing it")
    func modeIsSharedAcrossWindows() async throws {
        let test = try await loadedModel()
        let first = WindowState()
        let second = WindowState()
        let elsewhere = WindowState()
        for window in [first, second, elsewhere] { test.model.registerWindow(window) }
        let ref = test.threadRef("thr1")
        let other = test.threadRef("thr2")
        await first.openThread(ref, in: test.model)
        await second.openThread(ref, in: test.model)
        await elsewhere.openThread(other, in: test.model)

        first.toggleViewMode(in: test.model)
        // Both windows on the thread open its terminal, without a second
        // server open; the window on another thread is untouched.
        await settle(until: { first.threadTerminals[ref] != nil && second.threadTerminals[ref] != nil })
        #expect(test.model.viewMode(for: ref) == .tui)
        #expect(test.model.viewMode(for: other) == .chat)
        #expect(first.threadTerminals[ref] == second.threadTerminals[ref])
        #expect(test.client.openThreadTerminalCount <= 2)
        #expect(elsewhere.threadTerminals.isEmpty)
    }

    @Test("in Chat, reconciliation tracks the linked terminal but never attaches it")
    func chatNeverAttachesOnReconcile() async throws {
        let client = ScriptableAppServerClient()
        let test = try await loadedModel(client)
        let state = WindowState()
        let ref = test.threadRef("thr1")
        await state.openThread(ref, in: test.model)

        // Another client (a TUI elsewhere) links a live terminal.
        let linked = Fixtures.terminal(id: "trm_linked", threadId: "thr1")
        client.terminals += [linked]
        client.threads = client.threads.map { thread in
            var thread = thread
            if thread.id == "thr1" { thread.linkedTerminalId = "trm_linked" }
            return thread
        }
        await test.runtime.refresh()
        state.reconcile(in: test.model)

        #expect(state.threadTerminals[ref] == test.terminalRef("trm_linked"))
        #expect(test.model.terminals.isEmpty)
    }
}

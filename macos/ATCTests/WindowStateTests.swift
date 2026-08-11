import Foundation
import SwiftUI
import Testing
import ATCAppServerAPI
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

    @Test("openThread selects the thread, adopts its project, and records its terminal")
    func openThreadSucceeds() async throws {
        let test = try await loadedModel()
        let state = WindowState()
        let ref = test.threadRef("thr1")

        await state.openThread(ref, in: test.model)

        #expect(state.selectedContent == .thread(ref))
        #expect(state.selectedThread == ref)
        #expect(state.activeProject == test.projectRef("prj"))
        let terminalRef = try #require(state.threadTerminals[ref])
        #expect(state.visibleTerminal == terminalRef)
        #expect(test.model.terminals[terminalRef] != nil)
        #expect(state.threadOpenErrors[ref] == nil)
        #expect(state.terminalFocusRequest > 0)
    }

    @Test("a failed open keeps the selection and records the error")
    func openThreadFailureKeepsSelection() async throws {
        let client = ScriptableAppServerClient()
        let test = try await loadedModel(client)
        let state = WindowState()
        let ref = test.threadRef("thr1")

        client.shouldFail = true
        await state.openThread(ref, in: test.model)

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
        await state.openThread(ref, in: test.model)

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
        await state.openThread(ref, in: test.model)
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
        await state.openThread(ref, in: test.model)
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
        await state.openThread(ref, in: test.model)
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
        await state.openThread(ref, in: test.model)
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
        await state.openThread(ref, in: test.model)
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

    @Test("terminal focus is only requested while a terminal is visible")
    func focusRequestsRequireATerminal() async throws {
        let test = try await loadedModel()
        let state = WindowState()
        state.requestTerminalFocus()
        #expect(state.terminalFocusRequest == 0)

        state.selectTerminal(test.terminalRef("trm_live"), in: test.model)
        let after = state.terminalFocusRequest
        #expect(after > 0)
        state.requestTerminalFocus()
        #expect(state.terminalFocusRequest == after + 1)
    }
}

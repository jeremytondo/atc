import Foundation
import Testing
import ATCAppServerAPI
@testable import ATC

/// The LRU attachment budget: attaching past the budget evicts the
/// least-recently-used terminal through the standard disconnect path, while
/// the terminal the window is currently showing is never evicted.
@MainActor
@Suite("Attachment budget")
struct AttachmentBudgetTests {
    private func liveTerminal(_ id: String) -> Terminal {
        Fixtures.terminal(id: id, createdAt: Fixtures.date(0))
    }

    @Test("attaching past the budget evicts the least-recently-used terminal")
    func evictsLRU() throws {
        let test = try makeModel(attachmentBudget: 2)
        for index in 1...3 {
            test.model.attachIfNeeded(to: liveTerminal("t\(index)"), connectionID: test.connectionID)
        }
        #expect(test.model.terminals.count == 2)
        #expect(test.model.terminals[test.terminalRef("t1")] == nil)
        #expect(test.model.terminals[test.terminalRef("t2")] != nil)
        #expect(test.model.terminals[test.terminalRef("t3")] != nil)
    }

    @Test("touching a terminal refreshes its LRU position")
    func touchRefreshesLRU() throws {
        let test = try makeModel(attachmentBudget: 2)
        test.model.attachIfNeeded(to: liveTerminal("t1"), connectionID: test.connectionID)
        test.model.attachIfNeeded(to: liveTerminal("t2"), connectionID: test.connectionID)

        test.model.touchTerminal(test.terminalRef("t1"))
        test.model.attachIfNeeded(to: liveTerminal("t3"), connectionID: test.connectionID)
        #expect(test.model.terminals[test.terminalRef("t1")] != nil)
        #expect(test.model.terminals[test.terminalRef("t2")] == nil)
    }

    @Test("the visible terminal is never evicted")
    func visibleTerminalIsPinned() throws {
        let test = try makeModel(attachmentBudget: 2)
        let visible = test.terminalRef("t1")
        test.model.attachIfNeeded(to: liveTerminal("t1"), connectionID: test.connectionID)
        test.model.attachIfNeeded(to: liveTerminal("t2"), connectionID: test.connectionID)
        test.model.attachIfNeeded(
            to: liveTerminal("t3"),
            connectionID: test.connectionID,
            retentionContext: TerminalRetentionContext(visibleTerminal: visible)
        )
        #expect(test.model.terminals.count == 2)
        #expect(test.model.terminals[visible] != nil)
        // t2 — the oldest unpinned attach — was the eviction victim.
        #expect(test.model.terminals[test.terminalRef("t2")] == nil)
    }

    @Test("the visible terminal and the newest attach may together exceed the budget")
    func pinnedRefsExceedBudget() throws {
        let test = try makeModel(attachmentBudget: 1)
        let visible = test.terminalRef("t1")
        test.model.attachIfNeeded(to: liveTerminal("t1"), connectionID: test.connectionID)
        test.model.attachIfNeeded(
            to: liveTerminal("t2"),
            connectionID: test.connectionID,
            retentionContext: TerminalRetentionContext(visibleTerminal: visible)
        )
        #expect(test.model.terminals.count == 2)

        // Moving on unpins t1; the next attach evicts back down to budget.
        test.model.attachIfNeeded(to: liveTerminal("t3"), connectionID: test.connectionID)
        #expect(test.model.terminals.count == 1)
        #expect(test.model.terminals[test.terminalRef("t3")] != nil)
    }

    @Test("eviction goes through the standard disconnect path")
    func evictionDisconnects() throws {
        let test = try makeModel(attachmentBudget: 1)
        test.model.attachIfNeeded(to: liveTerminal("t1"), connectionID: test.connectionID)
        let ref = test.terminalRef("t1")
        let controller = try #require(test.model.terminals[ref])
        #expect(controller.isActivelyAttached)

        test.model.attachIfNeeded(to: liveTerminal("t2"), connectionID: test.connectionID)
        #expect(test.model.terminals[ref] == nil)
        // The evicted controller was disconnected, not leaked live.
        #expect(!controller.isActivelyAttached)
    }

    @Test("reselecting an evicted terminal reattaches through attachIfNeeded")
    func evictedTerminalReattaches() throws {
        let test = try makeModel(attachmentBudget: 1)
        test.model.attachIfNeeded(to: liveTerminal("t1"), connectionID: test.connectionID)
        test.model.attachIfNeeded(to: liveTerminal("t2"), connectionID: test.connectionID)
        let ref = test.terminalRef("t1")
        #expect(test.model.terminals[ref] == nil)

        test.model.attachIfNeeded(to: liveTerminal("t1"), connectionID: test.connectionID)
        #expect(test.model.terminals[ref] != nil)
        #expect(test.model.terminals.count == 1)
    }

    @Test("a disconnect releases the controller, and a later attach recreates it")
    func disconnectReleasesController() throws {
        let test = try makeModel()
        test.model.attachIfNeeded(to: liveTerminal("t1"), connectionID: test.connectionID)
        let ref = test.terminalRef("t1")

        test.model.disconnectTerminal(ref: ref)
        #expect(test.model.terminals[ref] == nil)

        test.model.attachIfNeeded(to: liveTerminal("t1"), connectionID: test.connectionID)
        #expect(test.model.terminals[ref] != nil)
    }
}

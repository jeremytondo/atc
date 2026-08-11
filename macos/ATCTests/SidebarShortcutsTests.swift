import Foundation
import Testing
import ATCAppServerAPI
@testable import ATC

/// The jump-shortcut ordering authority: numbering follows what the collapse
/// state reveals, capped at nine, and dispatch honors the same gates the
/// sidebar renders under.
@MainActor
@Suite("Sidebar jump shortcuts")
struct SidebarShortcutsTests {
    private let connectionID = UUID()

    private func model(
        threads: [ATCThread],
        filter: ThreadFilter = .all
    ) -> ThreadListModel {
        ThreadListModel(
            inputs: [.init(
                connectionID: connectionID,
                connectionName: "A",
                projects: [Fixtures.project(id: "prj")],
                threads: threads,
                archivedThreads: []
            )],
            filter: filter
        )
    }

    private func ref(_ id: String) -> ThreadRef {
        ThreadRef(connectionID: connectionID, threadID: id)
    }

    // MARK: - Stroke matching

    @Test("only exact ⌘digit and ⌥⌘digit map to jumps")
    func strokeMatching() {
        #expect(SidebarShortcuts.jump(for: KeyStroke(key: "1", modifiers: [.command]))
            == .thread(slot: 0))
        #expect(SidebarShortcuts.jump(for: KeyStroke(key: "9", modifiers: [.command, .option]))
            == .terminal(slot: 8))
        #expect(SidebarShortcuts.jump(for: KeyStroke(key: "0", modifiers: [.command])) == nil)
        #expect(SidebarShortcuts.jump(for: KeyStroke(key: "1", modifiers: [])) == nil)
        #expect(SidebarShortcuts.jump(for: KeyStroke(key: "1", modifiers: [.command, .shift]))
            == nil)
        #expect(SidebarShortcuts.jump(for: KeyStroke(key: "1", modifiers: [.option])) == nil)
        #expect(SidebarShortcuts.jump(for: KeyStroke(key: "b", modifiers: [.command])) == nil)
    }

    // MARK: - Thread ordering

    @Test("pinned rows precede recent and collapsed sections stop at five")
    func threadOrdering() {
        let threads = (1...7).map {
            Fixtures.thread(id: "pin\($0)", pinnedAt: Fixtures.date(Double($0)))
        } + (1...3).map {
            Fixtures.thread(id: "rec\($0)", createdAt: Fixtures.date(100 + Double($0)))
        }
        let collapsed = SidebarShortcuts.threadTargets(
            model: model(threads: threads),
            isPinnedExpanded: false,
            isRecentExpanded: false
        )
        // Five visible pinned rows (oldest pin first), then recent rows
        // newest-created-first; rows behind "N more" get no number.
        #expect(collapsed == [
            ref("pin1"), ref("pin2"), ref("pin3"), ref("pin4"), ref("pin5"),
            ref("rec3"), ref("rec2"), ref("rec1"),
        ])

        let expanded = SidebarShortcuts.threadTargets(
            model: model(threads: threads),
            isPinnedExpanded: true,
            isRecentExpanded: false
        )
        // Expansion reveals all seven pinned rows and the cap trims the
        // numbered list to nine.
        #expect(expanded.count == 9)
        #expect(expanded[6] == ref("pin7"))
        #expect(expanded.last == ref("rec2"))
    }

    @Test("the archived filter numbers only pinned rows")
    func archivedFilter() {
        let threads = [
            Fixtures.thread(id: "pin1", pinnedAt: Fixtures.date(1)),
            Fixtures.thread(id: "rec1", createdAt: Fixtures.date(2)),
        ]
        let targets = SidebarShortcuts.threadTargets(
            model: model(threads: threads, filter: .archived),
            isPinnedExpanded: false,
            isRecentExpanded: false
        )
        #expect(targets == [ref("pin1")])
    }

    // MARK: - Terminal ordering

    @Test("terminals number in section order, capped at nine, only while expanded")
    func terminalOrdering() {
        let terminals = (1...12).map {
            Fixtures.terminal(id: "trm\($0)", createdAt: Fixtures.date(Double($0)))
        }
        #expect(SidebarShortcuts.terminalTargets(terminals, isSectionExpanded: false).isEmpty)
        let expanded = SidebarShortcuts.terminalTargets(terminals, isSectionExpanded: true)
        #expect(expanded.map(\.id) == (1...9).map { "trm\($0)" })
    }

    // MARK: - Badge labels

    @Test("badge labels use the glyphs in Apple modifier order")
    func badgeLabels() {
        #expect(SidebarShortcuts.threadBadgeLabel(3) == "⌘3")
        #expect(SidebarShortcuts.terminalBadgeLabel(3) == "⌥⌘3")
    }

    // MARK: - Dispatch

    @Test("perform opens the numbered thread and selects the numbered terminal")
    func performJumps() async throws {
        let client = ScriptableAppServerClient()
        Fixtures.seed(client)
        let test = try await makeModel(client: client)
        let state = WindowState()

        // Recent ordering is newest-created-first: thr3, thr2, thr1.
        SidebarShortcuts.perform(.thread(slot: 0), appModel: test.model, windowState: state)
        await settle(until: { state.selectedContent == .thread(test.threadRef("thr3")) })
        #expect(state.selectedContent == .thread(test.threadRef("thr3")))
        #expect(state.activeProject == test.projectRef("prj"))

        // Standalone terminals oldest-first: trm_live, trm_ended.
        SidebarShortcuts.perform(.terminal(slot: 1), appModel: test.model, windowState: state)
        #expect(state.selectedContent == .terminal(test.terminalRef("trm_ended")))
    }

    @Test("terminal jumps require a collapsed section to be expanded and a project context")
    func terminalJumpGates() async throws {
        let client = ScriptableAppServerClient()
        Fixtures.seed(client)
        let test = try await makeModel(client: client)
        let state = WindowState()

        // No active project yet: nothing to jump to.
        SidebarShortcuts.perform(.terminal(slot: 0), appModel: test.model, windowState: state)
        #expect(state.selectedContent == .dashboard)

        SidebarShortcuts.perform(.thread(slot: 0), appModel: test.model, windowState: state)
        await settle(until: { state.selectedThread != nil })

        state.isTerminalsSectionExpanded = false
        SidebarShortcuts.perform(.terminal(slot: 0), appModel: test.model, windowState: state)
        #expect(state.selectedThread != nil)

        state.isTerminalsSectionExpanded = true
        SidebarShortcuts.perform(.terminal(slot: 0), appModel: test.model, windowState: state)
        #expect(state.selectedContent == .terminal(test.terminalRef("trm_live")))
    }

    @Test("out-of-range slots do nothing")
    func outOfRangeSlots() async throws {
        let client = ScriptableAppServerClient()
        Fixtures.seed(client)
        let test = try await makeModel(client: client)
        let state = WindowState()

        SidebarShortcuts.perform(.thread(slot: 8), appModel: test.model, windowState: state)
        await drainPendingTasks()
        #expect(state.selectedContent == .dashboard)
    }
}

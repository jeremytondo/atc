// The hardcoded sidebar jump shortcuts (⌘1–9 threads, ⌥⌘1–9 terminals):
// one ordering authority that feeds both the badges the sidebar renders and
// the dispatch the keyboard router performs, so display and behavior cannot
// drift.
//
// Numbering is what the collapse state reveals, in render order: visible
// pinned rows first, then visible recent rows, capped at nine; terminals are
// the active project's standalone terminals, gated on the section being
// expanded. The shortcuts deliberately do not consult sidebar visibility —
// muscle memory keeps working while the sidebar is hidden (⌘B), using the
// exact ordering the sidebar would show.

import ATCAppServerAPI
import Foundation

enum SidebarJump: Equatable, Sendable {
    /// Zero-based slot: ⌘1 is `.thread(slot: 0)`.
    case thread(slot: Int)
    case terminal(slot: Int)
}

enum SidebarShortcuts {
    /// Rows a sidebar section shows before its More control takes over.
    static let initialRowLimit = 5
    /// ⌘1…⌘9 — there is no ⌘0 and nothing past nine.
    static let slotCount = 9

    /// The jump a stroke maps to, on an exact modifier match only: ⌘ alone
    /// targets threads, ⌥⌘ targets terminals, anything else is no jump.
    static func jump(for stroke: KeyStroke) -> SidebarJump? {
        guard let digit = Int(stroke.key), (1...9).contains(digit) else { return nil }
        if stroke.modifiers == [.command] { return .thread(slot: digit - 1) }
        if stroke.modifiers == [.command, .option] { return .terminal(slot: digit - 1) }
        return nil
    }

    static func limited<T>(_ items: [T], expanded: Bool) -> [T] {
        expanded ? items : Array(items.prefix(initialRowLimit))
    }

    /// Threads in sidebar render order — visible pinned rows, then visible
    /// recent rows. Under the Archived filter the model's recent list is
    /// empty, so only pinned rows are numbered; archived rows never are.
    static func threadTargets(
        model: ThreadListModel,
        isPinnedExpanded: Bool,
        isRecentExpanded: Bool
    ) -> [ThreadRef] {
        let rows = limited(model.pinned, expanded: isPinnedExpanded)
            + limited(model.recent, expanded: isRecentExpanded)
        return rows.prefix(slotCount).map(\.ref)
    }

    /// The already-ordered standalone terminals, numbered only while the
    /// Terminals section is expanded.
    static func terminalTargets(
        _ terminals: [Terminal],
        isSectionExpanded: Bool
    ) -> [Terminal] {
        guard isSectionExpanded else { return [] }
        return Array(terminals.prefix(slotCount))
    }

    // Nonisolated: pure formatting, usable from nonisolated closure contexts
    // such as `Optional.map` without an isolation diagnostic.
    nonisolated static func threadBadgeLabel(_ number: Int) -> String {
        KeyStroke(key: "\(number)", modifiers: [.command]).displayDescription
    }

    nonisolated static func terminalBadgeLabel(_ number: Int) -> String {
        KeyStroke(key: "\(number)", modifiers: [.command, .option]).displayDescription
    }

    @MainActor
    static func perform(_ jump: SidebarJump, appModel: AppModel, windowState: WindowState) {
        switch jump {
        case .thread(let slot):
            let model = ThreadListModel(
                inputs: appModel.runtimes.map(ThreadListModel.ConnectionInput.init(runtime:)),
                filter: windowState.threadFilter
            )
            let targets = threadTargets(
                model: model,
                isPinnedExpanded: windowState.isPinnedExpanded,
                isRecentExpanded: windowState.isRecentExpanded
            )
            guard targets.indices.contains(slot) else { return }
            let ref = targets[slot]
            Task { await windowState.openThread(ref, in: appModel) }
        case .terminal(let slot):
            guard let project = windowState.activeProject,
                  let store = appModel.runtime(id: project.connectionID)?.terminals
            else { return }
            let targets = terminalTargets(
                store.standaloneTerminals(projectID: project.projectID),
                isSectionExpanded: windowState.isTerminalsSectionExpanded
            )
            guard targets.indices.contains(slot) else { return }
            windowState.selectTerminal(
                TerminalRef(connectionID: project.connectionID, terminalID: targets[slot].id),
                in: appModel
            )
        }
    }
}

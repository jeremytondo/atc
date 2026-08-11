import ATCAppServerAPI
import Foundation

enum PaletteResultID: Hashable {
    case command(CommandID)
    case thread(ThreadRef)
    case terminal(TerminalRef)
}

enum PaletteResult: Identifiable {
    case command(CommandPaletteRow)
    case thread(ThreadResult)
    case terminal(TerminalResult)

    var id: PaletteResultID {
        switch self {
        case .command(let row): .command(row.id)
        case .thread(let row): row.id
        case .terminal(let row): row.id
        }
    }
}

struct CommandPaletteRow: Identifiable {
    let id: CommandID
    let title: String
    let matchedRanges: [Range<String.Index>]
    let shortcut: KeyStroke?
    let availability: CommandAvailability
}

struct ThreadResult: Identifiable {
    let ref: ThreadRef
    let title: String
    /// Project (and Connection when ambiguous).
    let subtitle: String
    let matchedRanges: [Range<String.Index>]
    let availability: CommandAvailability
    var id: PaletteResultID { .thread(ref) }
}

struct TerminalResult: Identifiable {
    let ref: TerminalRef
    let title: String
    let subtitle: String
    let matchedRanges: [Range<String.Index>]
    var id: PaletteResultID { .terminal(ref) }
}

/// A whole-query type keyword for the unscoped palette: a trimmed query of
/// three or more characters that is a case-insensitive prefix of one plural
/// keyword ("threads", "terminals") lists every target of that type,
/// additively with ordinary matching. Anything else — shorter, longer than the
/// keyword, or a composed query like "thread parser" — matches only by title.
enum PaletteTypeKeyword: CaseIterable, Equatable {
    case threads
    case terminals

    static func match(_ query: String) -> PaletteTypeKeyword? {
        let query = query
            .trimmingCharacters(in: .whitespacesAndNewlines)
            .lowercased()
        guard query.count >= 3 else { return nil }
        return allCases.first { $0.keyword.hasPrefix(query) }
    }

    private var keyword: String {
        switch self {
        case .threads: "threads"
        case .terminals: "terminals"
        }
    }
}

@MainActor
enum CommandPaletteContent {
    static func results(
        query: String,
        keymap: ResolvedKeymap,
        context: CommandContext,
        presentation: CommandPalettePresentation
    ) -> [PaletteResult] {
        switch presentation {
        case .all:
            return allResults(query: query, keymap: keymap, context: context)
        case .threads:
            return threadResults(query: query, context: context, keyword: PaletteTypeKeyword.match(query))
                .map(PaletteResult.thread)
        case .terminals:
            return terminalResults(query: query, context: context, keyword: PaletteTypeKeyword.match(query))
                .map(PaletteResult.terminal)
        }
    }

    private static func allResults(
        query: String,
        keymap: ResolvedKeymap,
        context: CommandContext
    ) -> [PaletteResult] {
        let commands = commandRows(query: query, keymap: keymap, context: context)
            .map(PaletteResult.command)
        guard !query.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else {
            return commands
        }
        let keyword = PaletteTypeKeyword.match(query)
        let threads = threadResults(query: query, context: context, keyword: keyword)
            .map(PaletteResult.thread)
        let terminals = terminalResults(query: query, context: context, keyword: keyword)
            .map(PaletteResult.terminal)
        return commands + threads + terminals
    }

    /// Active threads only: an archived thread is a management row in the
    /// sidebar, never a navigation target.
    static func threadResults(
        query: String,
        context: CommandContext,
        keyword: PaletteTypeKeyword?
    ) -> [ThreadResult] {
        let model = context.appModel.threadList(filter: .all)
        let expands = keyword == .threads
        return (model.pinned + model.recent).compactMap { item in
            let titleMatch = QueryMatcher.match(query, in: item.thread.displayName)
            guard expands
                || titleMatch != nil
                || QueryMatcher.match(query, in: item.projectLabel) != nil
            else { return nil }
            return ThreadResult(
                ref: item.ref,
                title: item.thread.displayName,
                subtitle: item.projectLabel,
                matchedRanges: titleMatch?.ranges ?? [],
                availability: context.appModel.canMutate(connectionID: item.ref.connectionID)
                    ? .available
                    : .unavailable(reason: "Requires a reachable Connection")
            )
        }.sorted(by: threadComesFirst)
    }

    /// Standalone terminals only: a thread's TUI terminal is reached through
    /// its thread.
    static func terminalResults(
        query: String,
        context: CommandContext,
        keyword: PaletteTypeKeyword?
    ) -> [TerminalResult] {
        let expands = keyword == .terminals
        return context.appModel.runtimes.flatMap { runtime -> [TerminalResult] in
            runtime.terminals.terminals.compactMap { terminal in
                guard terminal.threadId == nil else { return nil }
                let projectName = runtime.projects.project(id: terminal.projectId)?.name
                    ?? "Unknown Project"
                let titleMatch = QueryMatcher.match(query, in: terminal.displayName)
                guard expands
                    || titleMatch != nil
                    || QueryMatcher.match(query, in: projectName) != nil
                else { return nil }
                return TerminalResult(
                    ref: TerminalRef(connectionID: runtime.id, terminalID: terminal.id),
                    title: terminal.displayName,
                    subtitle: terminal.isLive ? projectName : "\(projectName) · Ended",
                    matchedRanges: titleMatch?.ranges ?? []
                )
            }
        }.sorted(by: terminalComesFirst)
    }

    static func isOrderedBefore(
        title lhsTitle: String,
        id lhsID: CommandID,
        thanTitle rhsTitle: String,
        id rhsID: CommandID
    ) -> Bool {
        let lhs = lhsTitle.lowercased()
        let rhs = rhsTitle.lowercased()
        return lhs == rhs ? lhsID.rawValue < rhsID.rawValue : lhs < rhs
    }

    private static func commandRows(
        query: String,
        keymap: ResolvedKeymap,
        context: CommandContext
    ) -> [CommandPaletteRow] {
        CommandRegistry.allDescriptors.compactMap { descriptor in
            guard descriptor.isPaletteEligible,
                  let match = QueryMatcher.match(query, in: descriptor.title)
            else { return nil }
            return CommandPaletteRow(
                id: descriptor.id,
                title: descriptor.title,
                matchedRanges: match.ranges,
                shortcut: keymap.menuShortcuts[descriptor.id],
                availability: descriptor.availability(context)
            )
        }.sorted { isOrderedBefore(
            title: $0.title, id: $0.id,
            thanTitle: $1.title, id: $1.id
        ) }
    }

    /// Navigation rows order by title, then Connection, then the row's own
    /// server id — the only part that differs between threads and terminals.
    private static func navigationComesFirst(
        title lhsTitle: String,
        connectionID lhsConnection: UUID,
        id lhsID: String,
        thanTitle rhsTitle: String,
        connectionID rhsConnection: UUID,
        id rhsID: String
    ) -> Bool {
        let lhs = lhsTitle.lowercased()
        let rhs = rhsTitle.lowercased()
        if lhs != rhs { return lhs < rhs }
        let lhsConnectionID = lhsConnection.uuidString
        let rhsConnectionID = rhsConnection.uuidString
        if lhsConnectionID != rhsConnectionID { return lhsConnectionID < rhsConnectionID }
        return lhsID < rhsID
    }

    private static func threadComesFirst(_ lhs: ThreadResult, _ rhs: ThreadResult) -> Bool {
        navigationComesFirst(
            title: lhs.title, connectionID: lhs.ref.connectionID, id: lhs.ref.threadID,
            thanTitle: rhs.title, connectionID: rhs.ref.connectionID, id: rhs.ref.threadID
        )
    }

    private static func terminalComesFirst(_ lhs: TerminalResult, _ rhs: TerminalResult) -> Bool {
        navigationComesFirst(
            title: lhs.title, connectionID: lhs.ref.connectionID, id: lhs.ref.terminalID,
            thanTitle: rhs.title, connectionID: rhs.ref.connectionID, id: rhs.ref.terminalID
        )
    }
}

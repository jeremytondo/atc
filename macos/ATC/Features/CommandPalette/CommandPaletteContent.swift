import Foundation
import ATCAPI

enum PaletteResultID: Hashable {
    case command(CommandID)
    case workspace(WorkspaceRef)
    case session(SessionRef)
}

enum PaletteResult: Identifiable {
    case command(CommandPaletteRow)
    case workspace(WorkspaceResult)
    case session(SessionResult)

    var id: PaletteResultID {
        switch self {
        case .command(let row): .command(row.id)
        case .workspace(let row): row.id
        case .session(let row): row.id
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

struct WorkspaceResult: Identifiable {
    let ref: WorkspaceRef
    let title: String
    let projectName: String
    let connectionName: String
    let matchedRanges: [Range<String.Index>]
    let availability: CommandAvailability
    var id: PaletteResultID { .workspace(ref) }
}

struct SessionResult: Identifiable {
    let ref: SessionRef
    let title: String
    let kind: SessionKind
    let matchedRanges: [Range<String.Index>]
    var id: PaletteResultID { .session(ref) }
}

/// A whole-query type keyword for the unscoped palette: a trimmed query of
/// three or more characters that is a case-insensitive prefix of one plural
/// keyword ("sessions", "terminals", "workspaces") lists every target of that
/// type, additively with ordinary matching. Anything else — shorter, longer
/// than the keyword, or a composed query like "session parser" — matches only
/// by title.
enum PaletteTypeKeyword: CaseIterable, Equatable {
    case sessions
    case terminals
    case workspaces

    static func match(_ query: String) -> PaletteTypeKeyword? {
        let query = query
            .trimmingCharacters(in: .whitespacesAndNewlines)
            .lowercased()
        guard query.count >= 3 else { return nil }
        return allCases.first { $0.keyword.hasPrefix(query) }
    }

    private var keyword: String {
        switch self {
        case .sessions: "sessions"
        case .terminals: "terminals"
        case .workspaces: "workspaces"
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
        case .sessions:
            return scopedSessionResults(query: query, kind: .agent, context: context)
        case .terminals:
            return scopedSessionResults(query: query, kind: .terminal, context: context)
        case .workspaces:
            return workspaceResults(
                query: query,
                groups: ProjectsNavigatorGroups(runtimes: context.appModel.runtimes),
                keyword: nil
            ).map(PaletteResult.workspace)
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

        let workspaces = workspaceResults(
            query: query,
            groups: ProjectsNavigatorGroups(runtimes: context.appModel.runtimes),
            keyword: keyword
        ).map(PaletteResult.workspace)

        guard let activeWorkspace = context.windowState.activeWorkspace,
              let runtime = context.appModel.runtime(id: activeWorkspace.connectionID)
        else { return commands + workspaces }
        let sessions = sessionResults(
            query: query,
            activeWorkspace: activeWorkspace,
            sessions: runtime.sessions.sessions,
            keyword: keyword
        ).map(PaletteResult.session)
        return commands + workspaces + sessions
    }

    private static func scopedSessionResults(
        query: String,
        kind: SessionKind,
        context: CommandContext
    ) -> [PaletteResult] {
        guard let activeWorkspace = context.windowState.activeWorkspace,
              let runtime = context.appModel.runtime(id: activeWorkspace.connectionID)
        else { return [] }
        return sessionResults(
            query: query,
            activeWorkspace: activeWorkspace,
            sessions: runtime.sessions.sessions,
            keyword: nil,
            kind: kind
        ).map(PaletteResult.session)
    }

    static func workspaceResults(
        query: String,
        groups: ProjectsNavigatorGroups,
        keyword: PaletteTypeKeyword?
    ) -> [WorkspaceResult] {
        let expandsWorkspaces = keyword == .workspaces
        return groups.projects.flatMap { group in
            group.workspaces.compactMap { row in
                let titleMatch = QueryMatcher.match(query, in: row.workspace.name)
                guard expandsWorkspaces
                    || titleMatch != nil
                    || QueryMatcher.match(query, in: group.project.name) != nil
                    || QueryMatcher.match(query, in: group.connectionName) != nil
                else { return nil }
                return WorkspaceResult(
                    ref: row.ref,
                    title: row.workspace.name,
                    projectName: group.project.name,
                    connectionName: group.connectionName,
                    matchedRanges: titleMatch?.ranges ?? [],
                    availability: group.reachability == .connected
                        ? .available
                        : .unavailable(reason: "Requires a reachable Connection")
                )
            }
        }.sorted(by: workspaceComesFirst)
    }

    static func sessionResults(
        query: String,
        activeWorkspace: WorkspaceRef,
        sessions: [Session],
        keyword: PaletteTypeKeyword?,
        kind requiredKind: SessionKind? = nil
    ) -> [SessionResult] {
        return sessions.compactMap { session in
            guard session.belongs(to: activeWorkspace) else { return nil }
            let title = SessionKind.displayName(session: session)
            let kind = SessionKind.classify(session: session)
            guard requiredKind == nil || kind == requiredKind else { return nil }
            let titleMatch = QueryMatcher.match(query, in: title)
            let expandsKind: Bool
            switch kind {
            case .agent:
                expandsKind = keyword == .sessions
            case .terminal:
                expandsKind = keyword == .terminals
            }
            guard titleMatch != nil || expandsKind else { return nil }
            return SessionResult(
                ref: SessionRef(
                    connectionID: activeWorkspace.connectionID,
                    sessionID: session.id
                ),
                title: title,
                kind: kind,
                matchedRanges: titleMatch?.ranges ?? []
            )
        }.sorted(by: sessionComesFirst)
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

    private static func workspaceComesFirst(
        _ lhs: WorkspaceResult,
        _ rhs: WorkspaceResult
    ) -> Bool {
        let lhsTitle = lhs.title.lowercased()
        let rhsTitle = rhs.title.lowercased()
        if lhsTitle != rhsTitle { return lhsTitle < rhsTitle }
        let lhsConnection = lhs.ref.connectionID.uuidString
        let rhsConnection = rhs.ref.connectionID.uuidString
        if lhsConnection != rhsConnection { return lhsConnection < rhsConnection }
        return lhs.ref.workspaceID < rhs.ref.workspaceID
    }

    private static func sessionComesFirst(
        _ lhs: SessionResult,
        _ rhs: SessionResult
    ) -> Bool {
        let lhsTitle = lhs.title.lowercased()
        let rhsTitle = rhs.title.lowercased()
        return lhsTitle == rhsTitle
            ? lhs.ref.sessionID < rhs.ref.sessionID
            : lhsTitle < rhsTitle
    }
}

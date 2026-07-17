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

@MainActor
enum CommandPaletteContent {
    static func results(
        query: String,
        keymap: ResolvedKeymap,
        context: CommandContext
    ) -> [PaletteResult] {
        let commands = commandRows(query: query, keymap: keymap, context: context)
            .map(PaletteResult.command)
        guard !query.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else {
            return commands
        }

        let workspaces = workspaceResults(
            query: query,
            groups: ProjectsNavigatorGroups(runtimes: context.appModel.runtimes)
        ).map(PaletteResult.workspace)

        guard let activeWorkspace = context.windowState.activeWorkspace,
              let runtime = context.appModel.runtime(id: activeWorkspace.connectionID)
        else { return commands + workspaces }
        let sessions = sessionResults(
            query: query,
            activeWorkspace: activeWorkspace,
            sessions: runtime.sessions.sessions,
            actions: runtime.actions.actions
        ).map(PaletteResult.session)
        return commands + workspaces + sessions
    }

    static func workspaceResults(
        query: String,
        groups: ProjectsNavigatorGroups
    ) -> [WorkspaceResult] {
        groups.projects.flatMap { group in
            group.workspaces.compactMap { row in
                let titleMatch = QueryMatcher.match(query, in: row.workspace.name)
                guard titleMatch != nil
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
        actions: [ATCAction]
    ) -> [SessionResult] {
        sessions.compactMap { session in
            guard session.belongs(to: activeWorkspace), !session.isArchived else { return nil }
            let title = SessionKind.displayName(session: session, actions: actions)
            guard let match = QueryMatcher.match(query, in: title) else { return nil }
            return SessionResult(
                ref: SessionRef(
                    connectionID: activeWorkspace.connectionID,
                    sessionID: session.id
                ),
                title: title,
                kind: SessionKind.classify(session: session, actions: actions),
                matchedRanges: match.ranges
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

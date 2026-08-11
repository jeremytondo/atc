// Pure projection and interaction rules for the New Thread launcher, kept
// out of the view so filtering/ordering, keyboard navigation, and the agent
// preserve/adopt rules are directly testable.
//
// Ordering is the design's: with an empty query the contextual Project leads
// and the rest are alphabetical; with a query, name matches lead (still
// alphabetical) and rows matched only through their Connection name or
// default working directory follow. Matching uses the bare project name, not
// the sidebar label — the label suffixes a Connection name onto duplicates,
// which would wrongly promote connection-name queries to primary matches
// (the launcher shows the Connection as its own column instead).

import ATCAppServerAPI
import Foundation

enum NewThreadLauncherModel {
    /// One project result: the option plus highlight ranges in its project
    /// name. Secondary matches (connection name, working directory) carry no
    /// name highlights.
    struct Row: Identifiable, Equatable {
        let option: ProjectOption
        let nameRanges: [Range<String.Index>]
        var id: ProjectRef { option.ref }
    }

    static func rows(
        options: [ProjectOption],
        query: String,
        contextProject: ProjectRef?
    ) -> [Row] {
        let sorted = options.sorted {
            switch $0.project.name.localizedStandardCompare($1.project.name) {
            case .orderedAscending: return true
            case .orderedDescending: return false
            case .orderedSame:
                return $0.connectionName.localizedStandardCompare($1.connectionName)
                    == .orderedAscending
            }
        }
        let trimmed = query.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else {
            let context = sorted.filter { $0.ref == contextProject }
            let rest = sorted.filter { $0.ref != contextProject }
            return (context + rest).map { Row(option: $0, nameRanges: []) }
        }

        var nameMatches: [Row] = []
        var secondaryMatches: [Row] = []
        for option in sorted {
            if let match = QueryMatcher.match(trimmed, in: option.project.name) {
                nameMatches.append(Row(option: option, nameRanges: match.ranges))
                continue
            }
            let secondary = [option.connectionName, option.project.defaultWorkingDirectory]
            if secondary.contains(where: { QueryMatcher.match(trimmed, in: $0) != nil }) {
                secondaryMatches.append(Row(option: option, nameRanges: []))
            }
        }
        return nameMatches + secondaryMatches
    }

    /// Arrow / Ctrl-N / Ctrl-P movement: wraps at both ends; a selection not
    /// in the current rows enters from the end the motion came from.
    static func movedSelection(
        from current: ProjectRef?,
        by offset: Int,
        in rows: [Row]
    ) -> ProjectRef? {
        guard !rows.isEmpty else { return nil }
        guard let current, let index = rows.firstIndex(where: { $0.id == current }) else {
            return offset > 0 ? rows.first?.id : rows.last?.id
        }
        return rows[(index + offset + rows.count) % rows.count].id
    }

    /// Keep the current agent while the registry offers it as available;
    /// otherwise adopt the first available one. With nothing available the
    /// current selection stands — Create stays disabled and the reasons
    /// remain visible.
    static func resolvedAgent(preferring current: AgentID, in agents: [Agent]) -> AgentID {
        guard agents.first(where: { $0.id == current })?.available != true else { return current }
        return agents.first(where: \.available)?.id ?? current
    }

    /// Initial selection on open: the last-used agent when it is still
    /// offered, else the first available, else the first listed.
    static func initialAgent(lastUsedRawValue: String, agents: [Agent]) -> AgentID {
        if let lastUsed = AgentID(rawValue: lastUsedRawValue) {
            return resolvedAgent(preferring: lastUsed, in: agents)
        }
        return agents.first(where: \.available)?.id ?? agents.first?.id ?? .codex
    }
}

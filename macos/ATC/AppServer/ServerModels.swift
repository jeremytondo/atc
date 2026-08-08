// App-facing names for the generated App Server contract types, plus the
// tiny display derivations the UI needs everywhere. The generated
// `Components.Schemas.*` types are the domain model — nothing here wraps or
// re-validates them; `Thread` alone is renamed to avoid Foundation.Thread.

import ATCAppServerAPI
import Foundation

typealias ATCThread = Components.Schemas.Thread
typealias Project = Components.Schemas.Project
typealias Terminal = Components.Schemas.Terminal
typealias Agent = Components.Schemas.Agent
typealias AgentID = Components.Schemas.AgentId
typealias ThreadActivityState = Components.Schemas.ThreadActivityState
typealias TerminalStatus = Components.Schemas.TerminalStatus

extension AgentID {
    var displayName: String {
        switch self {
        case .codex: "Codex"
        case .claudeCode: "Claude Code"
        }
    }

    /// SF Symbol standing in for provider marks until real icons exist.
    var systemImage: String {
        switch self {
        case .codex: "hexagon"
        case .claudeCode: "asterisk"
        }
    }
}

extension ATCThread {
    /// Threads are commonly unnamed; the agent name keeps cards scannable.
    var displayName: String {
        if let name, !name.isEmpty { return name }
        return "\(agentId.displayName) Thread"
    }

    var isArchived: Bool { archivedAt != nil }
    var isPinned: Bool { pinnedAt != nil }
}

extension Terminal {
    /// Explicit title, else the command name, else Shell — per the design's
    /// "no scraped process information" rule.
    var displayName: String {
        if let name, !name.isEmpty { return name }
        if let command, let first = command.first {
            return URL(fileURLWithPath: first).lastPathComponent
        }
        return "Shell"
    }

    var isLive: Bool { status == .live }
}

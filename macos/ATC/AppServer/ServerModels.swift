// App-facing names for the generated App Server contract types, plus the
// tiny display derivations the UI needs everywhere. The generated
// `Components.Schemas.*` types are the domain model — nothing here wraps or
// re-validates them; `Thread` alone is renamed to avoid Foundation.Thread.

import ATCAppServerAPI
import Foundation
import SwiftUI

typealias ATCThread = Components.Schemas.Thread
typealias Project = Components.Schemas.Project
typealias Terminal = Components.Schemas.Terminal
typealias Agent = Components.Schemas.Agent
typealias AgentID = Components.Schemas.AgentId
typealias ThreadActivityState = Components.Schemas.ThreadActivityState
typealias TerminalStatus = Components.Schemas.TerminalStatus
typealias ThreadItem = Components.Schemas.ThreadItem
typealias ThreadTurn = Components.Schemas.ThreadTurn
typealias ThreadRequest = Components.Schemas.ThreadRequest
typealias ThreadRequestAnswer = Components.Schemas.ThreadRequestAnswer
typealias QueuedPrompt = Components.Schemas.QueuedPrompt
typealias ThreadEvent = Components.Schemas.ThreadEvent
typealias ThreadTranscriptPage = Components.Schemas.ThreadTranscript
typealias ToolStatus = Components.Schemas.ToolStatus
typealias ThreadSettings = Components.Schemas.ThreadSettings
typealias ThreadSettingsPatch = Components.Schemas.ThreadSettingsPatch
typealias ReasoningLevel = Components.Schemas.ReasoningLevel
typealias ThreadMode = Components.Schemas.ThreadMode
typealias ThreadAccess = Components.Schemas.ThreadAccess
typealias AgentModel = Components.Schemas.AgentModel

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

extension ReasoningLevel {
    var displayName: String {
        switch self {
        case .low: "Low"
        case .medium: "Medium"
        case .high: "High"
        case .xhigh: "Extra high"
        case .max: "Max"
        case .ultra: "Ultra"
        }
    }
}

extension ThreadMode {
    var displayName: String {
        switch self {
        case .chat: "Chat"
        case .plan: "Plan"
        }
    }
}

extension ThreadAccess {
    var displayName: String {
        switch self {
        case .supervised: "Supervised"
        case .autoAcceptEdits: "Auto-accept edits"
        case .auto: "Auto"
        case .fullAccess: "Full access"
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

    /// An idle thread whose finish nobody viewed yet reads "Done" (ATC-160).
    /// A display-only translation of `idle + unread` — the server vocabulary
    /// has no completed state, and the server alone decides `unread`.
    private var showsDone: Bool { activityState == .idle && unread }

    /// Card status text; prefer these over the raw `activityState` labels so
    /// the Done overlay applies uniformly.
    var statusLabel: String? { showsDone ? "Done" : activityState.statusLabel }

    /// The inspector's and toolbar's longer phrasing (see
    /// `ThreadActivityState.detailLabel`).
    var detailLabel: String { showsDone ? "Done" : activityState.detailLabel }

    /// Done gets the app accent — deliberately distinct from Running green
    /// and from Needs-you orange, so orange keeps its one meaning.
    var statusColor: Color { showsDone ? .accentColor : activityState.statusColor }
}

extension ThreadActivityState {
    /// Card status text per the design comps; `unknown` is deliberately
    /// unlabeled. The wording differs per state so color is never the only
    /// signal.
    var statusLabel: String? {
        switch self {
        case .working: "Running"
        case .needsInput: "Needs you"
        case .idle: "Idle"
        case .unknown: nil
        }
    }

    /// The inspector's and toolbar's longer phrasing for the same states.
    /// Deliberately worded differently from `statusLabel`: a card is scanned,
    /// a detail row is read.
    var detailLabel: String {
        switch self {
        case .working: "Working"
        case .needsInput: "Needs input"
        case .idle: "Idle"
        case .unknown: "Unknown"
        }
    }

    var statusColor: Color {
        switch self {
        case .working: .green
        case .needsInput: .orange
        case .idle, .unknown: .secondary
        }
    }
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

extension ThreadItem {
    /// Item id, unique within the thread — the upsert key.
    var id: String { head.id }
    var turnId: String { head.turnId }
    /// The enclosing item when this work happened inside a subagent or
    /// nested tool; absent at top level.
    var parentItemId: String? { head.parentItemId }

    /// The fields every item variant carries, read through one switch.
    private var head: (id: String, turnId: String, parentItemId: String?) {
        switch self {
        case .userMessage(let item): (item.id, item.turnId, item.parentItemId)
        case .assistantText(let item): (item.id, item.turnId, item.parentItemId)
        case .reasoning(let item): (item.id, item.turnId, item.parentItemId)
        case .command(let item): (item.id, item.turnId, item.parentItemId)
        case .fileChange(let item): (item.id, item.turnId, item.parentItemId)
        case .mcpCall(let item): (item.id, item.turnId, item.parentItemId)
        case .toolCall(let item): (item.id, item.turnId, item.parentItemId)
        case .compaction(let item): (item.id, item.turnId, item.parentItemId)
        }
    }

    /// Extends a still-streaming text item (`text.delta`). A completed item
    /// already holds its whole text (a delta replayed behind the page that
    /// completed it must not double it); other items ignore deltas.
    mutating func appendText(_ delta: String) {
        switch self {
        case .assistantText(var item) where !item.complete:
            item.text += delta
            self = .assistantText(item)
        case .reasoning(var item) where !item.complete:
            item.text += delta
            self = .reasoning(item)
        case .assistantText, .reasoning, .userMessage, .command, .fileChange, .mcpCall, .toolCall, .compaction:
            break
        }
    }
}

extension ThreadRequest {
    var id: String {
        switch self {
        case .question(let request): request.id
        case .approval(let request): request.id
        }
    }
}

extension ThreadEvent {
    /// The durable position of this change; nil for live-only events (text
    /// deltas, requests, the queue), which are never replayed.
    var seq: Int? {
        switch self {
        case .item_started(let event): event.seq
        case .item_updated(let event): event.seq
        case .item_completed(let event): event.seq
        case .turn_started(let event): event.seq
        case .turn_completed(let event): event.seq
        case .snapshot_invalidated(let event): event.seq
        case .text_delta, .request_opened, .request_closed, .queue_updated: nil
        }
    }
}

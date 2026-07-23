import Foundation
import ATCAPI

/// The one user-facing identity projection for a Session.
///
/// Agent Sessions keep their launch identity visible and use a custom name as
/// supporting context. Terminals use a custom name as their primary label,
/// falling back to their Action name or "Shell" when unnamed.
struct SessionIdentity: Equatable, Sendable {
    let index: Int?
    let kind: SessionKind
    let identityText: String
    let customName: String?

    init(session: Session) {
        index = session.sessionIndex
        kind = SessionKind.classify(session: session)
        if session.actionId == nil {
            identityText = "Shell"
        } else {
            // An Action-backed Session should always carry its copied name.
            // Keep a useful non-category fallback for malformed legacy data.
            identityText = session.actionName?.nonemptyTrimmed ?? "Action"
        }
        customName = session.name?.nonemptyTrimmed
    }

    /// The visually prominent portion of the label.
    var primaryText: String {
        if kind == .terminal, let customName {
            return customName
        }
        return identityText
    }

    /// Supporting context shown only for named Agent Sessions.
    var secondaryText: String? {
        kind == .agent ? customName : nil
    }

    /// The complete visible label, without the index badge.
    var fullLabel: String {
        secondaryText.map { "\(primaryText) · \($0)" } ?? primaryText
    }

    /// Plain-text equivalent of the badge and label used in dialogs/help.
    var indexedLabel: String {
        index.map { "[\($0)] \(fullLabel)" } ?? fullLabel
    }

    /// Launch identity with its index, for metadata surfaces that expose the
    /// immutable identity separately from the custom display name.
    var indexedIdentityLabel: String {
        index.map { "[\($0)] \(identityText)" } ?? identityText
    }

    var accessibilityLabel: String {
        var parts: [String] = []
        if let index {
            parts.append("Session \(index)")
        }
        parts.append(primaryText)
        if let secondaryText {
            parts.append(secondaryText)
        }
        return parts.joined(separator: ", ")
    }
}

/// The shared Workspace grouping and ordering used by both navigation
/// surfaces. Session indexes share one namespace, so gaps within either
/// visible group are intentional.
struct WorkspaceSessionGroups: Equatable, Sendable {
    struct Row: Identifiable, Equatable, Sendable {
        let ref: SessionRef
        let session: Session
        let identity: SessionIdentity
        let kind: SessionKind

        var id: SessionRef { ref }
    }

    let sessions: [Row]
    let terminals: [Row]

    static let empty = WorkspaceSessionGroups(sessions: [], terminals: [])

    init(workspace: WorkspaceRef, sessions allSessions: [Session]) {
        let rows = allSessions
            .filter { $0.belongs(to: workspace) }
            .sortedBySessionIndex()
            .map { session in
                let identity = SessionIdentity(session: session)
                return Row(
                    ref: SessionRef(
                        connectionID: workspace.connectionID,
                        sessionID: session.id
                    ),
                    session: session,
                    identity: identity,
                    kind: identity.kind
                )
            }
        sessions = rows.filter { $0.kind == .agent }
        terminals = rows.filter { $0.kind == .terminal }
    }

    private init(sessions: [Row], terminals: [Row]) {
        self.sessions = sessions
        self.terminals = terminals
    }
}

private extension String {
    var nonemptyTrimmed: String? {
        let trimmed = trimmingCharacters(in: .whitespacesAndNewlines)
        return trimmed.isEmpty ? nil : trimmed
    }
}

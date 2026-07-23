import Foundation
import ATCAPI

/// The one user-facing identity projection for a Session.
///
/// A custom name is supporting context. It never replaces the launch
/// identity copied onto the Session.
struct SessionIdentity: Equatable, Sendable {
    let index: Int?
    let identityText: String
    let customName: String?

    init(session: Session) {
        index = session.sessionIndex
        if session.actionId == nil {
            identityText = "Shell"
        } else {
            // An Action-backed Session should always carry its copied name.
            // Keep a useful non-category fallback for malformed legacy data.
            identityText = session.actionName?.nonemptyTrimmed ?? "Action"
        }
        customName = session.name?.nonemptyTrimmed
    }

    /// Identity plus optional supporting name, without the index badge.
    var fullLabel: String {
        customName.map { "\(identityText) · \($0)" } ?? identityText
    }

    /// Plain-text equivalent of the badge and label used in dialogs/help.
    var indexedLabel: String {
        index.map { "[\($0)] \(fullLabel)" } ?? fullLabel
    }

    var accessibilityLabel: String {
        var parts: [String] = []
        if let index {
            parts.append("Session \(index)")
        }
        parts.append(identityText)
        if let customName {
            parts.append(customName)
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
                Row(
                    ref: SessionRef(
                        connectionID: workspace.connectionID,
                        sessionID: session.id
                    ),
                    session: session,
                    identity: SessionIdentity(session: session),
                    kind: SessionKind.classify(session: session)
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

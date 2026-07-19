import Foundation
import ATCAPI

/// How a Session is presented: Sessions launched from an Agent Action are
/// labeled **Session** (agent); everything else — the Interactive Shell,
/// general Actions, and Actions that no longer resolve in the registry —
/// is labeled **Terminal**. Both remain the one generic `Session` model;
/// this is a pure client-side join against the Connection's action list,
/// so a misclassification after an Action delete is cosmetic and accepted.
enum SessionKind: Equatable {
    case agent
    case terminal

    static func classify(session: Session, actions: [ATCAction]) -> SessionKind {
        guard let actionName = session.action,
              let action = actions.first(where: { $0.name == actionName })
        else { return .terminal }
        return action.isAgent ? .agent : .terminal
    }

    /// The one display-name rule for rows, headers, and confirmations:
    /// a user-given name wins; an unnamed Interactive Shell reads
    /// "Terminal"; an unnamed Action session reads its Action's label
    /// (falling back to the raw action name when it no longer resolves).
    static func displayName(session: Session, actions: [ATCAction]) -> String {
        if let name = session.name, !name.isEmpty { return name }
        guard let actionName = session.action else { return "Terminal" }
        return actions.first(where: { $0.name == actionName })?.displayLabel ?? actionName
    }

    /// The toolbar pill identifies an agent session by its Agent Action and
    /// a terminal session by its session display name.
    static func toolbarLabel(session: Session, actions: [ATCAction]) -> String {
        guard classify(session: session, actions: actions) == .agent,
              let actionName = session.action
        else { return displayName(session: session, actions: actions) }
        return actions.first(where: { $0.name == actionName })?.displayLabel ?? actionName
    }
}

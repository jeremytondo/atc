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

    /// The Action label shown next to the name in the content header:
    /// "Terminal" for the Interactive Shell, else the Action's label.
    static func actionLabel(session: Session, actions: [ATCAction]) -> String {
        guard let actionName = session.action else { return "Terminal" }
        return actions.first(where: { $0.name == actionName })?.displayLabel ?? actionName
    }
}

import Foundation
import ATCAPI

/// Presentation classification copied onto each Session at launch. It never
/// depends on the current Actions list, so later Action changes cannot alter
/// an existing session's identity.
enum SessionKind: Equatable, Sendable {
    case agent
    case terminal

    static func classify(session: Session) -> SessionKind {
        guard session.actionId != nil else { return .terminal }
        return session.isAgent ? .agent : .terminal
    }
}

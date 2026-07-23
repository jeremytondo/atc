import Foundation
import ATCAPI

/// Shared creation metadata used by Workspace ordering and the legacy
/// fallback for index-less Sessions.
protocol CreatedOrdered {
    var createdAt: Date { get }
    var id: String { get }
}

extension Session: CreatedOrdered {}
extension Workspace: CreatedOrdered {}

extension Sequence where Element: CreatedOrdered {
    func sortedNewestFirst() -> [Element] {
        sorted {
            if $0.createdAt != $1.createdAt { return $0.createdAt > $1.createdAt }
            return $0.id < $1.id
        }
    }
}

extension Sequence where Element == Session {
    /// Workspace Session address order: indexed Sessions first and ascending,
    /// then legacy index-less Sessions by creation time and stable ID.
    func sortedBySessionIndex() -> [Session] {
        sorted {
            switch ($0.sessionIndex, $1.sessionIndex) {
            case let (lhs?, rhs?) where lhs != rhs:
                return lhs < rhs
            case (_?, nil):
                return true
            case (nil, _?):
                return false
            default:
                if $0.createdAt != $1.createdAt {
                    return $0.createdAt < $1.createdAt
                }
                return $0.id < $1.id
            }
        }
    }
}

extension Session {
    var isActive: Bool { status == .live }

    /// Whether this Session belongs to the Workspace. Server IDs are only
    /// unique within a Connection, so callers compare same-Connection refs.
    func belongs(to ref: WorkspaceRef) -> Bool {
        workspace?.id == ref.workspaceID
    }
}

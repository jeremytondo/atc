import Foundation
import ATCAPI

/// The one display order for Workspace and Session lists: newest-created
/// first, with a stable id tiebreak so equal timestamps keep a
/// deterministic order across renders.
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

extension Session {
    /// Whether this Session belongs to the Workspace. Server IDs are only
    /// unique within a Connection, so callers compare same-Connection refs.
    func belongs(to ref: WorkspaceRef) -> Bool {
        workspace?.id == ref.workspaceID
    }
}

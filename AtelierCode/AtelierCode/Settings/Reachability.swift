import SwiftUI

/// The most recent reachability outcome for a Connection. Phase 3 derives this
/// from each per-Connection refresh completion; until then everything is
/// `.unknown`.
enum Reachability {
    case unknown
    case connected
    case unreachable

    /// Status-dot color: gray (no result yet), green (reachable), red (failed).
    var color: Color {
        switch self {
        case .unknown: return .secondary
        case .connected: return .green
        case .unreachable: return .red
        }
    }
}

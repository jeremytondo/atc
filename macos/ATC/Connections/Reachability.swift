import SwiftUI

/// The most recent reachability outcome for a Connection: gray until the
/// event stream and first refresh settle, then green/red from there.
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

    /// The dot is color-only; assistive tech gets the state in words.
    var accessibilityDescription: String {
        switch self {
        case .unknown: "status unknown"
        case .connected: "reachable"
        case .unreachable: "unreachable"
        }
    }
}

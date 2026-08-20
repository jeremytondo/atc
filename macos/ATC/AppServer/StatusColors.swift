// The SwiftUI color reads of the shared status vocabulary (ServerModels in
// ATCAppServerAPI keeps the names and labels; color is a client styling
// choice).

import ATCAppServerAPI
import SwiftUI

extension ATCThread {
    /// Done gets the app accent — deliberately distinct from Running green
    /// and from Needs-you orange, so orange keeps its one meaning.
    var statusColor: Color { showsDone ? .accentColor : activityState.statusColor }
}

extension ThreadActivityState {
    var statusColor: Color {
        switch self {
        case .working: .green
        case .needsInput: .orange
        case .idle, .unknown: .secondary
        }
    }
}

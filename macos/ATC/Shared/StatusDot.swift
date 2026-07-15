import SwiftUI

/// The one reachability/liveness dot used across the app.
struct StatusDot: View {
    enum Size {
        /// 6pt — inside captions and chips.
        case inline
        /// 8pt — list rows and section headers.
        case standard

        var diameter: CGFloat {
            switch self {
            case .inline: 6
            case .standard: 8
            }
        }
    }

    let color: Color
    var size: Size = .standard
    /// Hollow ring for "present but inactive" (e.g. no active sessions).
    var hollow = false

    var body: some View {
        Circle()
            .fill(hollow ? Color.clear : color)
            .overlay {
                if hollow {
                    Circle().stroke(.tertiary, lineWidth: 2)
                }
            }
            .frame(width: size.diameter, height: size.diameter)
    }
}

import SwiftUI

/// The one reachability dot used across the app. Takes the state, not a
/// color, so the color and its spoken description cannot drift apart.
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

    let reachability: Reachability
    var size: Size = .standard

    var body: some View {
        Circle()
            .fill(reachability.color)
            .frame(width: size.diameter, height: size.diameter)
            .accessibilityLabel("Connection \(reachability.accessibilityDescription)")
    }
}

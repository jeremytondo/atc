import SwiftUI
import ATCAPI

/// Colored status dot with optional label.
struct StatusBadge: View {
    let session: Session
    var showLabel = false

    var body: some View {
        HStack(spacing: Spacing.xs) {
            StatusDot(color: color)
            if showLabel {
                Text(label)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
        .accessibilityLabel(label)
    }

    private var color: Color {
        switch session.status {
        case .live: return .green
        case .ended: return .red
        }
    }

    private var label: String { session.status.rawValue.capitalized }
}

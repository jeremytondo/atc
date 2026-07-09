import SwiftUI
import AtelierCodeAPI

/// Colored status dot with optional label.
struct StatusBadge: View {
    let session: Session
    var showLabel = false

    var body: some View {
        HStack(spacing: 4) {
            Circle()
                .fill(color)
                .frame(width: 8, height: 8)
            if showLabel {
                Text(label)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
        .accessibilityLabel(label)
    }

    private var color: Color {
        if session.isArchived { return .gray.opacity(0.5) }
        switch session.status {
        case .running: return .green
        case .starting: return .yellow
        case .failed: return .red
        case .terminated: return .gray
        }
    }

    private var label: String {
        session.isArchived ? "archived" : session.status.rawValue
    }
}

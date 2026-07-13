import SwiftUI

/// Neutral chip naming the Connection a row belongs to, with the
/// reachability dot. Shared by Dashboard and Workspace-scoped UI; the
/// chip never uses color to identify the Connection.
struct ConnectionChip: View {
    let name: String
    let reachability: Reachability

    var body: some View {
        HStack(spacing: Spacing.xs) {
            StatusDot(color: reachability.color, size: .inline)
            Text(name)
                .font(.caption2)
                .foregroundStyle(.secondary)
                .lineLimit(1)
        }
        .padding(.horizontal, 6)
        .padding(.vertical, 2)
        .background(.quaternary.opacity(0.5), in: Capsule())
    }
}

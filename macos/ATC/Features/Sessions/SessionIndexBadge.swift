import SwiftUI

/// Workspace-local Session address. This is identity, never status, so its
/// appearance deliberately does not vary with lifecycle or selection.
struct SessionIndexBadge: View {
    let index: Int

    init(_ index: Int) {
        self.index = index
    }

    var body: some View {
        Text(String(index))
            .font(.caption2.monospaced().weight(.semibold))
            .foregroundStyle(.secondary)
            .padding(.horizontal, Spacing.xs)
            .frame(minWidth: 18, minHeight: 16)
            .background(
                .quaternary,
                in: RoundedRectangle(cornerRadius: Spacing.xs)
            )
            .accessibilityHidden(true)
    }
}

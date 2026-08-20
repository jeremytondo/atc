import ATCDesign
import SwiftUI

/// The ⌘N / ⌥⌘N chip shown while the exact modifier combo is held. Matches
/// the card action chips' height so swapping it in never resizes a row.
struct ShortcutBadge: View {
    let label: String

    var body: some View {
        Text(label)
            .font(.caption.weight(.semibold))
            .foregroundStyle(.secondary)
            .padding(.horizontal, Spacing.xs)
            .frame(height: NavigatorMetrics.actionSize)
            .background(
                Surface.raised,
                in: RoundedRectangle(cornerRadius: Radius.control, style: .continuous)
            )
    }
}

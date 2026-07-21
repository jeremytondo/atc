import SwiftUI

/// The one text pill: a secondary caption in a quaternary capsule.
/// Used for action labels and origin/context tags.
struct TagBadge: View {
    let text: String
    /// Monospaced-semibold variant for code-like tags (LOCAL/REMOTE).
    var monospaced = false

    var body: some View {
        Text(text)
            .font(monospaced ? .caption2.monospaced().weight(.semibold) : .caption2)
            .foregroundStyle(.secondary)
            .padding(.horizontal, 6)
            .padding(.vertical, 2)
            .background(.quaternary, in: Capsule())
    }
}

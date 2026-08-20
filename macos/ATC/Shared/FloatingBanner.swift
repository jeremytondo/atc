import ATCDesign
import SwiftUI

/// The content area's floating status pill: a glass capsule pinned to the
/// top of whatever it overlays (terminal, chat). Every transient state
/// banner — connecting, reconnecting, ended, open errors — is one of these,
/// so they all sit in the same place with the same look.
struct FloatingBanner<Content: View>: View {
    @ViewBuilder let content: () -> Content

    var body: some View {
        HStack(spacing: Spacing.sm, content: content)
            .padding(.horizontal, Spacing.md)
            .padding(.vertical, Spacing.sm)
            // Floating overlay on Liquid Glass, like the toolbar controls.
            .glassEffect()
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
            .padding(.top, Spacing.lg)
    }
}

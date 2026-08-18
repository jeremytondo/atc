// The window toolbar floats over the canvas with its background hidden
// (RootView, ATC-41). On macOS 26 that also drops the toolbar's own scroll
// edge effect, so content would scroll under the toolbar controls unfaded.
// SwiftUI does draw its soft edge effect — the system's progressive blur and
// fade — under any `safeAreaBar` that holds Liquid Glass, and the effect
// spans the whole top safe area, toolbar included. So a scroll view under the
// toolbar gets a 1pt bar of identity glass: nothing to see, but the system
// takes over the fade. (A clear color or empty view in the bar is not enough;
// verified against the macOS 26.5 SDK.)

import SwiftUI

extension View {
    /// Fades scrolling content under the floating window toolbar with the
    /// system's soft scroll edge effect. Apply to the `ScrollView` itself.
    func scrollEdgeEffectUnderToolbar() -> some View {
        safeAreaBar(edge: .top) {
            Color.clear
                .frame(maxWidth: .infinity)
                .frame(height: 1)
                .glassEffect(.identity)
        }
    }
}

import ATCAPI

/// Owns the detail-area canvas color: the terminal's backing color when a
/// terminal is the visible content, the app canvas for any cover.
@MainActor
enum DetailCanvas {
    /// A terminal is the visible content only when a live selected session
    /// has a retained controller and the dashboard is not covering it — the
    /// same conditions under which SessionContentView draws no opaque cover.
    static func showsTerminal(
        isDashboard: Bool,
        session: Session?,
        hasController: Bool
    ) -> Bool {
        !isDashboard && session?.status == .live && hasController
    }

    static func backingColor(
        showsTerminal: Bool,
        preferences: TerminalPreferences
    ) -> TerminalBackingColor {
        showsTerminal
            ? TerminalPresentation.backingColor(preferences: preferences)
            : AppColors.canvasBackingColor
    }
}

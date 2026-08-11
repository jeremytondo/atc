import SwiftUI

/// The app-wide dark canvas. `canvasHex` is the single source; the SwiftUI
/// color and the terminal's injected background both derive from it so every
/// surface rests on one color.
enum AppColors {
    static let canvasHex = "141416"
    static let canvas = color(fromHex: canvasHex)

    /// Expects a compile-time-trusted RGB hex literal, not user input.
    private static func color(fromHex hex: String) -> Color {
        precondition(hex.utf8.count == 6, "Canvas color must be RGB hex")
        return Color(
            red: Double(UInt8(hex.prefix(2), radix: 16)!) / 255,
            green: Double(UInt8(hex.dropFirst(2).prefix(2), radix: 16)!) / 255,
            blue: Double(UInt8(hex.dropFirst(4).prefix(2), radix: 16)!) / 255
        )
    }
}

/// The app-wide spacing scale (4pt grid). Use these instead of literal
/// spacing/padding values; keep literal `2` only for tight two-line text
/// stacks and pill interiors.
enum Spacing {
    /// Intra-label gaps (dot–text).
    static let xs: CGFloat = 4
    /// Control clusters, row internals.
    static let sm: CGFloat = 8
    /// Standard container/bar padding.
    static let md: CGFloat = 12
    /// Card interior padding.
    static let lg: CGFloat = 16
    /// Dashboard page margins / section gaps.
    static let xxl: CGFloat = 32
}

/// Corner radii: small controls, chip controls, cards, and floating panels.
/// Text pills use `Capsule`.
enum Radius {
    static let control: CGFloat = 6
    static let chip: CGFloat = 8
    static let card: CGFloat = 12
    /// Floating panels over the app (the Command Palette).
    static let panel: CGFloat = 11
}

/// The neutral overlay surfaces on the dark canvas. Every raised element —
/// chip, badge, card — is white at a fixed opacity, so the scale lives here
/// rather than in each view.
enum Surface {
    /// Bordered chip controls at rest, and their border.
    static let chip = Color.white.opacity(0.06)
    static let chipBorder = Color.white.opacity(0.12)
    /// Badges, card action chips, and a card under the pointer.
    static let raised = Color.white.opacity(0.07)
    /// A card at rest, and its border.
    static let card = Color.white.opacity(0.035)
    static let cardBorder = Color.white.opacity(0.08)
}

/// Opacity applied to unavailable content.
enum Dimming {
    static let unavailable: Double = 0.5
}

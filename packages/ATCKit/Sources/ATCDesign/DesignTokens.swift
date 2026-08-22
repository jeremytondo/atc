import SwiftUI

/// The app-wide dark canvas. `canvasHex` is the single source; the SwiftUI
/// color and the terminal's injected background both derive from it so every
/// surface rests on one color.
public enum AppColors {
    public static let canvasHex = "141416"
    public static let canvas = color(fromHex: canvasHex)

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
public enum Spacing {
    /// Intra-label gaps (dot–text).
    public static let xs: CGFloat = 4
    /// Control clusters, row internals.
    public static let sm: CGFloat = 8
    /// Standard container/bar padding.
    public static let md: CGFloat = 12
    /// Card interior padding.
    public static let lg: CGFloat = 16
    /// Dashboard page margins / section gaps.
    public static let xxl: CGFloat = 32
}

/// Chat prose — assistant text and the user's own messages. Sized to T3Code's
/// chat (14px at a ~1.6 line-height) rather than the 13pt system body: one
/// point up keeps it a clear step above the 12pt `.callout` chrome (tool
/// rows, chips, captions) without drifting from the system scale. Headings
/// land on system styles where one fits (`.title2` 17, `.title3` 15) and on
/// the prose size for h4+, so a heading never reads smaller than its body.
public enum Prose {
    public static let size: CGFloat = 14
    public static let font = Font.system(size: size)
    /// Extra points between wrapped lines, on top of the font's natural
    /// ~17pt line box — together they land at T3Code's 1.625 ratio.
    public static let lineSpacing: CGFloat = 6

    /// The font for a markdown heading at `level` (1-based; 4 and deeper
    /// share the prose size in semibold).
    public static func heading(_ level: Int) -> Font {
        switch level {
        case 1: .system(size: 20, weight: .semibold)
        case 2: .title2.weight(.semibold)
        case 3: .title3.weight(.semibold)
        default: font.weight(.semibold)
        }
    }
}

/// Corner radii: small controls, chip controls, cards, and floating panels.
/// Text pills use `Capsule`.
public enum Radius {
    public static let control: CGFloat = 6
    public static let chip: CGFloat = 8
    public static let card: CGFloat = 12
    /// The command palette's floating panel (other overlays keep their
    /// own radii deliberately).
    public static let panel: CGFloat = 11
}

/// The neutral overlay surfaces on the dark canvas. Every raised element —
/// chip, badge, card — is white at a fixed opacity, so the scale lives here
/// rather than in each view.
public enum Surface {
    /// Bordered chip controls at rest, and their border.
    public static let chip = Color.white.opacity(0.06)
    public static let chipBorder = Color.white.opacity(0.12)
    /// Badges, card action chips, and a card under the pointer.
    public static let raised = Color.white.opacity(0.07)
    /// A card at rest, and its border.
    public static let card = Color.white.opacity(0.035)
    public static let cardBorder = Color.white.opacity(0.08)
}

/// The text hierarchy. Three tiers, one rule: the user's own words, headings,
/// and control labels are `primary`; prose to be read is `body`; context about
/// the prose — metadata, placeholders, tool detail — is `secondary`. All three
/// derive from the system label color so Increase Contrast still lifts them.
/// On the canvas they measure ~13:1, ~11:1, and ~6:1; `.tertiary` (2.2:1) and
/// `.quaternary` fall below the 4.5:1 floor here, so they are reserved for
/// non-text — dividers, fills, and disabled glyphs — never for readable text.
public enum TextColor {
    public static let primary = Color.primary
    /// Long-form assistant prose, one step below headings so a screen of text
    /// does not glare. Matches the prose tier T3Code and Codex desktop use.
    public static let body = Color.primary.opacity(0.9)
    public static let secondary = Color.secondary
}

/// Opacity applied to unavailable content.
public enum Dimming {
    public static let unavailable: Double = 0.5
}

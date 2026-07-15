import SwiftUI

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

/// Corner radii: small controls and cards. Text pills use `Capsule`.
enum Radius {
    static let control: CGFloat = 6
    static let card: CGFloat = 12
}

/// Opacity applied to archived/unavailable content.
enum Dimming {
    static let archived: Double = 0.5
}

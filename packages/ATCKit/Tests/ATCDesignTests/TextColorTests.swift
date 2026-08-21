import AppKit
import SwiftUI
import Testing

@testable import ATCDesign

/// The text ladder's two invariants, measured the way the app renders them
/// (dark appearance, composited over the real surfaces): every readable tier
/// clears the HIG's 4.5:1 floor, and the tiers stay in order, so a tier cannot
/// drift dim or swap places without a failing test.
@Suite("Text tiers")
struct TextColorTests {
    private let tiers: [(name: String, color: Color)] = [
        ("primary", TextColor.primary),
        ("body", TextColor.body),
        ("secondary", TextColor.secondary),
    ]

    @Test("every text tier clears 4.5:1 on the canvas and on a card")
    func contrast() {
        let canvas = composite(resolve(AppColors.canvas), over: (0, 0, 0))
        let card = composite(resolve(Surface.card), over: canvas)
        for tier in tiers {
            let ink = resolve(tier.color)
            #expect(contrast(composite(ink, over: canvas), canvas) >= 4.5, "\(tier.name) on canvas")
            #expect(contrast(composite(ink, over: card), card) >= 4.5, "\(tier.name) on card")
        }
    }

    @Test("tiers descend in brightness: primary > body > secondary")
    func ordering() {
        let canvas = composite(resolve(AppColors.canvas), over: (0, 0, 0))
        let luminances = tiers.map { luminance(composite(resolve($0.color), over: canvas)) }
        #expect(luminances == luminances.sorted(by: >))
        #expect(Set(luminances).count == tiers.count)
    }

    // MARK: - Color math (sRGB, WCAG 2.x relative luminance)

    private typealias RGBA = (r: Double, g: Double, b: Double, a: Double)
    private typealias RGB = (r: Double, g: Double, b: Double)

    private func resolve(_ color: Color) -> RGBA {
        var resolved: RGBA = (0, 0, 0, 0)
        NSAppearance(named: .darkAqua)!.performAsCurrentDrawingAppearance {
            let ns = NSColor(color).usingColorSpace(.sRGB)!
            resolved = (ns.redComponent, ns.greenComponent, ns.blueComponent, ns.alphaComponent)
        }
        return resolved
    }

    private func composite(_ top: RGBA, over bottom: RGB) -> RGB {
        (
            top.r * top.a + bottom.r * (1 - top.a),
            top.g * top.a + bottom.g * (1 - top.a),
            top.b * top.a + bottom.b * (1 - top.a)
        )
    }

    private func luminance(_ c: RGB) -> Double {
        func channel(_ v: Double) -> Double {
            v <= 0.03928 ? v / 12.92 : pow((v + 0.055) / 1.055, 2.4)
        }
        return 0.2126 * channel(c.r) + 0.7152 * channel(c.g) + 0.0722 * channel(c.b)
    }

    private func contrast(_ a: RGB, _ b: RGB) -> Double {
        let (la, lb) = (luminance(a), luminance(b))
        return (max(la, lb) + 0.05) / (min(la, lb) + 0.05)
    }
}

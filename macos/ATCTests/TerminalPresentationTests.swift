import Testing
@testable import ATC

@MainActor
@Suite("Terminal presentation")
struct TerminalPresentationTests {
    @Test("unset preferences render safe padding defaults")
    func defaultPaddingRendering() {
        #expect(TerminalPresentation.renderedConfiguration(
            preferences: TerminalPreferences()
        ) == """
        window-padding-x = 8
        window-padding-y = 6
        """)
    }

    @Test("generated preferences render with padding defaults or overrides")
    func individualRendering() {
        let cases: [(TerminalPreferences, String)] = [
            (
                .init(fontFamily: "Berkeley Mono"),
                "font-family = Berkeley Mono\nwindow-padding-x = 8\nwindow-padding-y = 6"
            ),
            (
                .init(fontSize: 14),
                "font-size = 14\nwindow-padding-x = 8\nwindow-padding-y = 6"
            ),
            (
                .init(paddingX: 12),
                "window-padding-x = 12\nwindow-padding-y = 6"
            ),
            (
                .init(paddingY: 9),
                "window-padding-x = 8\nwindow-padding-y = 9"
            ),
        ]

        for (preferences, expected) in cases {
            #expect(TerminalPresentation.renderedConfiguration(
                preferences: preferences
            ) == expected)
        }
    }

    @Test("explicit zero padding renders edge-to-edge overrides")
    func zeroPaddingRendering() {
        #expect(TerminalPresentation.renderedConfiguration(
            preferences: TerminalPreferences(paddingX: 0, paddingY: 0)
        ) == """
        window-padding-x = 0
        window-padding-y = 0
        """)
    }

    @Test("theme is resolved separately and does not emit a config key")
    func themeRendering() {
        let rendered = TerminalPresentation.renderedConfiguration(
            preferences: TerminalPreferences(theme: "Catppuccin Mocha")
        )
        #expect(rendered == """
        window-padding-x = 8
        window-padding-y = 6
        """)
        #expect(!rendered.contains("theme"))
    }

    @Test("unset preferences apply safe padding and the app-owned canvas background")
    func compiledDefaultsController() {
        // Unset preferences keep libghostty's compiled defaults except for
        // safe content padding and the app-owned background.
        let controller = TerminalPresentation.makeController(preferences: .init())
        #expect(controller.renderedConfig == """
        window-padding-x = 8
        window-padding-y = 6
        background = 141416

        """)
    }

    @Test("a selected theme keeps its palette but the canvas background wins")
    func themeBackgroundIsAlwaysCanvas() {
        let controller = TerminalPresentation.makeController(preferences: .init(
            theme: "Catppuccin Mocha"
        ))
        let lines = controller.renderedConfig.split(separator: "\n")
        // The theme's own background may render first; the app-owned canvas
        // must be the value that ends up applied.
        #expect(lines.last { $0.hasPrefix("background =") } == "background = 141416")
        #expect(controller.renderedConfig.contains("palette ="))
    }
}

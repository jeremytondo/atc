import Testing
@testable import ATC

@MainActor
@Suite("Terminal presentation")
struct TerminalPresentationTests {
    @Test("unset preferences render no wrapper-selected configuration")
    func emptyRendering() {
        #expect(TerminalPresentation.renderedConfiguration(
            preferences: TerminalPreferences()
        ).isEmpty)
    }

    @Test("each generated preference renders only its Ghostty key")
    func individualRendering() {
        let cases: [(TerminalPreferences, String)] = [
            (.init(fontFamily: "Berkeley Mono"), "font-family = Berkeley Mono"),
            (.init(fontSize: 14), "font-size = 14"),
            (.init(paddingX: 8), "window-padding-x = 8"),
            (.init(paddingY: 9), "window-padding-y = 9"),
        ]

        for (preferences, expected) in cases {
            #expect(TerminalPresentation.renderedConfiguration(
                preferences: preferences
            ) == expected)
        }
    }

    @Test("theme is resolved separately and does not emit a config key")
    func themeRendering() {
        let rendered = TerminalPresentation.renderedConfiguration(
            preferences: TerminalPreferences(theme: "Catppuccin Mocha")
        )
        #expect(rendered.isEmpty)
        #expect(!rendered.contains("theme"))
    }

    @Test("unset preferences apply only the app-owned canvas background")
    func compiledDefaultsController() {
        // Unset preferences keep libghostty's compiled defaults except for
        // the app-owned background shared with every other surface.
        let controller = TerminalPresentation.makeController(preferences: .init())
        #expect(controller.renderedConfig == "background = 141416\n")
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

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
            (.init(backgroundOpacity: 0.95), "background-opacity = 0.95"),
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

    @Test("a fresh controller with no preferences applies no configuration keys")
    func compiledDefaultsController() {
        // Pins the ATC-5 requirement: unset preferences mean libghostty's
        // compiled defaults — neither the wrapper's default config nor its
        // default theme may leak into the applied configuration.
        let controller = TerminalPresentation.makeController(preferences: .init())
        #expect(controller.renderedConfig.isEmpty)
    }

    @Test("explicit background outranks the selected theme's background")
    func backgroundOverridesTheme() {
        let controller = TerminalPresentation.makeController(preferences: .init(
            theme: "Catppuccin Mocha",
            background: "ff0000"
        ))
        let lines = controller.renderedConfig.split(separator: "\n")
        #expect(lines.last { $0.hasPrefix("background =") } == "background = ff0000")
    }

    @Test("backing color prefers explicit background, then theme, then black")
    func backingColorResolution() {
        let explicit = TerminalPresentation.backingColor(preferences: .init(
            theme: "Catppuccin Mocha",
            background: "ff0000"
        ))
        #expect(explicit == TerminalBackingColor(red: 1, green: 0, blue: 0))

        let themed = TerminalPresentation.backingColor(
            preferences: .init(theme: "Catppuccin Mocha")
        )
        #expect(themed == TerminalBackingColor(
            red: 30.0 / 255,
            green: 30.0 / 255,
            blue: 46.0 / 255
        ))
        #expect(TerminalPresentation.backingColor(preferences: .init())
            == TerminalBackingColor(
                red: 40.0 / 255,
                green: 44.0 / 255,
                blue: 52.0 / 255
            ))
    }
}

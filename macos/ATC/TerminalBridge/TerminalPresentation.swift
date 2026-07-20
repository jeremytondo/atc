import GhosttyTerminal
import GhosttyTheme

@MainActor
enum TerminalPresentation {
    static func makeController(preferences: TerminalPreferences) -> TerminalController {
        // An empty generated base is libghostty's compiled defaults;
        // .none would substitute the wrapper's TerminalConfiguration.default
        // (cursor style, font-size 14, font-thicken) instead.
        TerminalController(
            configSource: .generated(""),
            theme: resolvedTheme(preferences: preferences),
            terminalConfiguration: configuration(preferences: preferences)
        )
    }

    /// Theme names are validated at configuration-load time so an invalid
    /// name rejects the whole candidate before anything is applied.
    nonisolated static func isKnownTheme(_ name: String) -> Bool {
        GhosttyThemeCatalog.theme(named: name) != nil
    }

    static func apply(
        preferences: TerminalPreferences,
        to controller: TerminalController
    ) {
        // The controller re-resolves base + configuration + theme and pushes
        // the result to the app and every retained surface in place.
        _ = controller.setTerminalConfiguration(configuration(preferences: preferences))
        _ = controller.setTheme(resolvedTheme(preferences: preferences))
    }

    static func renderedConfiguration(preferences: TerminalPreferences) -> String {
        configuration(preferences: preferences).rendered
    }

    private static func configuration(
        preferences: TerminalPreferences
    ) -> TerminalConfiguration {
        // TerminalConfiguration.default contains wrapper-selected values.
        TerminalConfiguration { builder in
            if let fontFamily = preferences.fontFamily {
                builder.withFontFamily(fontFamily)
            }
            if let fontSize = preferences.fontSize {
                builder.withFontSize(fontSize)
            }
            if let paddingX = preferences.paddingX {
                builder.withWindowPaddingX(paddingX)
            }
            if let paddingY = preferences.paddingY {
                builder.withWindowPaddingY(paddingY)
            }
            // background is rendered through the theme (themes render after
            // this configuration, so only a theme-level value wins).
        }
    }

    private static func resolvedTheme(preferences: TerminalPreferences) -> TerminalTheme {
        let theme: TerminalTheme
        if let themeName = preferences.theme {
            guard let definition = GhosttyThemeCatalog.theme(named: themeName) else {
                preconditionFailure("Terminal theme was not validated before application")
            }
            theme = definition.toTerminalTheme()
        } else {
            // Start empty so libghostty's compiled defaults stand except for
            // the app-owned background injected below.
            theme = TerminalTheme(light: .init(), dark: .init())
        }

        // The terminal background is always the app canvas so every surface
        // rests on one color; a theme contributes only its text palette.
        return TerminalTheme(
            light: TerminalConfiguration(startingFrom: theme.light) {
                $0.withBackground(AppColors.canvasHex)
            },
            dark: TerminalConfiguration(startingFrom: theme.dark) {
                $0.withBackground(AppColors.canvasHex)
            }
        )
    }
}

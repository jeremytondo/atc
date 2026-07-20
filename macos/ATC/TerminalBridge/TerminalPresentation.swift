import GhosttyTerminal
import GhosttyTheme

struct TerminalBackingColor: Equatable, Sendable {
    let red: Double
    let green: Double
    let blue: Double

    init(red: Double, green: Double, blue: Double) {
        self.red = red
        self.green = green
        self.blue = blue
    }

    /// Expects a compile-time-trusted RGB hex literal, not user input;
    /// user-supplied values are validated by ConfigurationLoader first.
    init(hex: String) {
        let normalized = hex.hasPrefix("#") ? String(hex.dropFirst()) : hex
        precondition(normalized.utf8.count == 6, "Terminal background must be RGB hex")
        red = Double(UInt8(normalized.prefix(2), radix: 16)!) / 255
        green = Double(UInt8(normalized.dropFirst(2).prefix(2), radix: 16)!) / 255
        blue = Double(UInt8(normalized.dropFirst(4).prefix(2), radix: 16)!) / 255
    }
}

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

    static func backingColor(preferences: TerminalPreferences) -> TerminalBackingColor {
        if let background = preferences.background {
            return TerminalBackingColor(hex: background)
        }
        if let themeName = preferences.theme,
           let theme = GhosttyThemeCatalog.theme(named: themeName) {
            return TerminalBackingColor(hex: theme.background)
        }
        return AppColors.canvasBackingColor
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
            if let backgroundOpacity = preferences.backgroundOpacity {
                builder.withBackgroundOpacity(backgroundOpacity)
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

        // A selected theme owns its background unless the user explicitly
        // overrides it. With no theme, ATC owns the default canvas color.
        let background = preferences.background
            ?? (preferences.theme == nil ? AppColors.canvasHex : nil)
        guard let background else { return theme }
        return TerminalTheme(
            light: TerminalConfiguration(startingFrom: theme.light) {
                $0.withBackground(background)
            },
            dark: TerminalConfiguration(startingFrom: theme.dark) {
                $0.withBackground(background)
            }
        )
    }
}

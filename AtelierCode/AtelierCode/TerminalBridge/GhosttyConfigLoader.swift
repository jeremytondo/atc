import Foundation
import GhosttyTerminal
import GhosttyTheme

/// Builds the shared Ghostty `TerminalController`, layering a best-effort
/// read of the user's Ghostty config over Catppuccin Mocha defaults.
///
/// Hand-parsing covers exactly the keys this POC honors (theme,
/// font-family, font-size, window-padding-*, background-opacity,
/// background); keybinds/splits are out of scope by design — the bytes
/// come over a WebSocket, not a local Ghostty.
enum GhosttyConfigLoader {
    struct UserConfig {
        var theme: String?
        var fontFamily: String?
        var fontSize: Float?
        var paddingX: Int?
        var paddingY: Int?
        var backgroundOpacity: Double?
        var background: String?
    }

    static func makeController() -> TerminalController {
        let user = loadUserConfig()

        // Per-key graceful fallback: any missing/unresolvable value drops
        // to the Mocha default, never fails surface creation.
        let themeDefinition = user.theme.flatMap { GhosttyThemeCatalog.theme(named: $0) }
            ?? GhosttyThemeCatalog.theme(named: "Catppuccin Mocha")
        let theme = themeDefinition?.toTerminalTheme() ?? .default

        let configuration = TerminalConfiguration(startingFrom: .default) { builder in
            if let fontFamily = user.fontFamily {
                // Unknown families fall back inside libghostty's font
                // discovery; never crashes surface creation.
                builder.withFontFamily(fontFamily)
            }
            if let fontSize = user.fontSize {
                builder.withFontSize(fontSize)
            }
            builder.withWindowPaddingX(user.paddingX ?? 6)
            builder.withWindowPaddingY(user.paddingY ?? 6)
            if let opacity = user.backgroundOpacity {
                builder.withBackgroundOpacity(opacity)
            }
            if let background = user.background {
                builder.withBackground(background)
            }
        }
        return TerminalController(configuration: configuration, theme: theme)
    }

    static func loadUserConfig() -> UserConfig {
        let home = FileManager.default.homeDirectoryForCurrentUser
        let candidates = [
            home.appending(path: ".config/ghostty/config"),
            home.appending(path: "Library/Application Support/com.mitchellh.ghostty/config"),
        ]
        guard let text = candidates.lazy
            .compactMap({ try? String(contentsOf: $0, encoding: .utf8) })
            .first
        else {
            return UserConfig()
        }
        return parse(text)
    }

    static func parse(_ text: String) -> UserConfig {
        var config = UserConfig()
        for rawLine in text.split(separator: "\n") {
            let line = rawLine.trimmingCharacters(in: .whitespaces)
            guard !line.isEmpty, !line.hasPrefix("#"), let eq = line.firstIndex(of: "=") else {
                continue
            }
            let key = line[..<eq].trimmingCharacters(in: .whitespaces)
            let value = line[line.index(after: eq)...].trimmingCharacters(in: .whitespaces)
            guard !value.isEmpty else { continue }

            switch key {
            case "theme":
                config.theme = themeName(from: value)
            case "font-family":
                config.fontFamily = value
            case "font-size":
                config.fontSize = Float(value)
            case "window-padding-x":
                config.paddingX = Int(value)
            case "window-padding-y":
                config.paddingY = Int(value)
            case "background-opacity":
                config.backgroundOpacity = Double(value)
            case "background":
                config.background = value
            default:
                break
            }
        }
        return config
    }

    /// `theme` can be a plain name or `light:Name,dark:Name`; the app is
    /// dark-only, so prefer the dark variant.
    private static func themeName(from value: String) -> String {
        guard value.contains(":") else { return value }
        for part in value.split(separator: ",") {
            let pair = part.split(separator: ":", maxSplits: 1)
            if pair.count == 2, pair[0].trimmingCharacters(in: .whitespaces) == "dark" {
                return pair[1].trimmingCharacters(in: .whitespaces)
            }
        }
        return value
    }
}

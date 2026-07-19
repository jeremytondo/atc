import Foundation
import Testing
@testable import ATC

@Suite("Configuration loader")
struct ConfigurationLoaderTests {
    @Test("supported tables decode with spec-compliant TOML strings and whitespace")
    func validRealTOML() throws {
        let parsed = ConfigurationLoader.parse(#"""
          [keyboard]
          leader = 'cmd+k'
          leader_timeout_ms=1_800
          clear_default_keybindings = false

          [keybindings]
          'cmd+b'='view.toggle-sidebar'
          'leader>b' = "view.toggle-sidebar"
        """#)

        #expect(parsed.diagnostics.isEmpty)
        #expect(parsed.tables["keyboard"]?.count == 3)
        #expect(parsed.tables["keybindings"]?.map(\.key) == ["cmd+b", "leader>b"])
        #expect(try Keymap.resolve(user: parsed).get().menuShortcuts[.toggleSidebar]
            == KeyStroke(key: "b", modifiers: .command))
    }

    @Test("unknown top-level tables are rejected")
    func unknownTable() {
        let parsed = ConfigurationLoader.parse(#"""
        [display]
        scale = 2
        """#)

        #expect(parsed.diagnostics.contains {
            $0.severity == .error
                && $0.message.contains("[display]")
                && $0.message.contains("not a recognized table")
        })
    }

    @Test("top-level bare keys are rejected")
    func topLevelKey() {
        let parsed = ConfigurationLoader.parse("leader = \"cmd+k\"")
        #expect(parsed.diagnostics.contains {
            $0.message.contains("Top-level key 'leader'")
        })
    }

    @Test("unknown keyboard keys are errors with their full path")
    func unknownKeyboardKey() {
        let parsed = ConfigurationLoader.parse(#"""
        [keyboard]
        typo = true
        """#)

        #expect(parsed.diagnostics == [.init(
            severity: .error,
            message: "[keyboard].typo is not recognized"
        )])
    }

    @Test("wrong scalar types name every affected table and key")
    func wrongTypes() {
        let parsed = ConfigurationLoader.parse(#"""
        [keyboard]
        leader = false
        leader_timeout_ms = 1.5
        clear_default_keybindings = "no"

        [keybindings]
        "cmd+b" = true
        """#)
        let messages = parsed.diagnostics.map(\.message)

        #expect(messages.contains {
            $0.contains("[keyboard].leader") && $0.contains("string")
        })
        #expect(messages.contains {
            $0.contains("[keyboard].leader_timeout_ms") && $0.contains("integer")
        })
        #expect(messages.contains {
            $0.contains("[keyboard].clear_default_keybindings")
                && $0.contains("boolean")
        })
        #expect(messages.contains {
            $0.contains(#"[keybindings]."cmd+b""#) && $0.contains("string")
        })
    }

    @Test("duplicate keys are TOML parser errors")
    func duplicateKey() {
        let parsed = ConfigurationLoader.parse(#"""
        [keybindings]
        "cmd+b" = "data.refresh"
        "cmd+b" = "view.toggle-sidebar"
        """#)

        #expect(parsed.tables.isEmpty)
        #expect(parsed.diagnostics.count == 1)
        #expect(parsed.diagnostics[0].severity == .error)
        // The decoder rejects the duplicate at its line; exact wording is
        // the parser's ("Ill-formed key" for quoted keys).
        #expect(parsed.diagnostics[0].message.contains("(Line 3)"))
    }

    @Test("TOML syntax errors retain the decoder's line context")
    func syntaxErrorLine() {
        let parsed = ConfigurationLoader.parse(#"""
        [keyboard]
        leader = "cmd+k"
        broken =
        """#)

        #expect(parsed.tables.isEmpty)
        #expect(parsed.diagnostics.count == 1)
        #expect(parsed.diagnostics[0].message.contains("Line 3"))
    }

    @Test("empty text and comments resolve as an empty optional configuration")
    func emptyConfig() {
        #expect(ConfigurationLoader.parse("").tables.isEmpty)
        #expect(ConfigurationLoader.parse("\n # comment\n").diagnostics.isEmpty)
        #expect(ConfigurationLoader.parse("").terminal == TerminalPreferences())
    }

    @Test("terminal table decodes every supported preference")
    func terminalHappyPath() {
        let parsed = ConfigurationLoader.parse(#"""
        [terminal]
        theme = "Catppuccin Mocha"
        font_family = "Berkeley Mono"
        font_size = 14.0
        padding_x = 8
        padding_y = 9
        background = "1e1e2e"
        background_opacity = 0.95
        """#)

        #expect(parsed.diagnostics.isEmpty)
        #expect(parsed.terminal == TerminalPreferences(
            theme: "Catppuccin Mocha",
            fontFamily: "Berkeley Mono",
            fontSize: 14,
            paddingX: 8,
            paddingY: 9,
            background: "1e1e2e",
            backgroundOpacity: 0.95
        ))
    }

    @Test("terminal accepts integer font size and normalizes prefixed background")
    func terminalNumericAndBackgroundNormalization() {
        let parsed = ConfigurationLoader.parse(#"""
        [terminal]
        font_size = 14
        background = "#A1b2C3"
        """#)

        #expect(parsed.diagnostics.isEmpty)
        #expect(parsed.terminal.fontSize == 14)
        #expect(parsed.terminal.background == "A1b2C3")
    }

    @Test("terminal rejects unknown keys and wrong scalar types with full paths")
    func terminalSchemaAndTypes() {
        let parsed = ConfigurationLoader.parse(#"""
        [terminal]
        typo = true
        theme = 1
        font_family = false
        font_size = "14"
        padding_x = 1.5
        padding_y = "8"
        background = 123456
        background_opacity = "opaque"
        """#)
        let messages = parsed.diagnostics.map(\.message)

        for key in [
            "typo", "theme", "font_family", "font_size", "padding_x",
            "padding_y", "background", "background_opacity",
        ] {
            #expect(messages.contains { $0.contains("[terminal].\(key)") })
        }
        #expect(messages.first { $0.contains("font_size") }?.contains("number") == true)
        #expect(messages.first { $0.contains("padding_x") }?.contains("integer") == true)
        #expect(messages.first { $0.contains("background_opacity") }?.contains("number") == true)
    }

    @Test("terminal rejects invalid numeric ranges")
    func terminalNumericRanges() {
        for value in ["0", "-1", "inf", "nan", "1e300"] {
            let parsed = ConfigurationLoader.parse("[terminal]\nfont_size = \(value)")
            #expect(parsed.diagnostics.contains {
                $0.message.contains("[terminal].font_size")
            })
        }

        for key in ["padding_x", "padding_y"] {
            let parsed = ConfigurationLoader.parse("[terminal]\n\(key) = -1")
            #expect(parsed.diagnostics.contains {
                $0.message.contains("[terminal].\(key)")
                    && $0.message.contains("non-negative")
            })
        }

        for value in ["-0.01", "1.01", "inf", "nan"] {
            let parsed = ConfigurationLoader.parse(
                "[terminal]\nbackground_opacity = \(value)"
            )
            #expect(parsed.diagnostics.contains {
                $0.message.contains("[terminal].background_opacity")
            })
        }

        for value in ["0.0", "1.0", "0", "1"] {
            #expect(ConfigurationLoader.parse(
                "[terminal]\nbackground_opacity = \(value)"
            ).diagnostics.isEmpty)
        }
    }

    @Test("terminal rejects malformed RGB backgrounds")
    func terminalBackgroundValidation() {
        for value in ["12345", "1234567", "12xx56", "##12345"] {
            let parsed = ConfigurationLoader.parse(
                "[terminal]\nbackground = \"\(value)\""
            )
            #expect(parsed.diagnostics.contains {
                $0.message.contains("[terminal].background")
                    && $0.message.contains("6-hex-digit")
            })
        }
    }

    @Test("terminal requires non-empty font family")
    func terminalEmptyFontFamily() {
        let parsed = ConfigurationLoader.parse(#"""
        [terminal]
        font_family = "   "
        """#)

        #expect(parsed.diagnostics == [.init(
            severity: .error,
            message: "[terminal].font_family must be a non-empty string"
        )])
    }

    @Test("terminal theme names are validated against the real catalog")
    func terminalThemeValidation() {
        #expect(ConfigurationLoader.parse(#"""
        [terminal]
        theme = "Catppuccin Mocha"
        """#).diagnostics.isEmpty)

        let unknown = ConfigurationLoader.parse(#"""
        [terminal]
        theme = "Definitely Not A Real Theme"
        """#)
        #expect(unknown.diagnostics == [.init(
            severity: .error,
            message: #"[terminal].theme "Definitely Not A Real Theme" is not a known theme"#
        )])
    }
}

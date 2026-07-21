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
          clear_default_keybindings = false

          [keybindings]
          'cmd+b'='view.toggle-sidebar'
          'leader>b' = "view.toggle-sidebar"
        """#)

        #expect(parsed.diagnostics.isEmpty)
        #expect(parsed.tables["keyboard"]?.count == 2)
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
        clear_default_keybindings = "no"

        [keybindings]
        "cmd+b" = true
        """#)
        let messages = parsed.diagnostics.map(\.message)

        #expect(messages.contains {
            $0.contains("[keyboard].leader") && $0.contains("string")
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
        """#)

        #expect(parsed.diagnostics.isEmpty)
        #expect(parsed.terminal == TerminalPreferences(
            theme: "Catppuccin Mocha",
            fontFamily: "Berkeley Mono",
            fontSize: 14,
            paddingX: 8,
            paddingY: 9
        ))
    }

    @Test("terminal accepts integer font size")
    func terminalIntegerFontSize() {
        let parsed = ConfigurationLoader.parse(#"""
        [terminal]
        font_size = 14
        """#)

        #expect(parsed.diagnostics.isEmpty)
        #expect(parsed.terminal.fontSize == 14)
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
        """#)
        let messages = parsed.diagnostics.map(\.message)

        for key in [
            "typo", "theme", "font_family", "font_size", "padding_x", "padding_y",
        ] {
            #expect(messages.contains { $0.contains("[terminal].\(key)") })
        }
        #expect(messages.first { $0.contains("font_size") }?.contains("number") == true)
        #expect(messages.first { $0.contains("padding_x") }?.contains("integer") == true)
    }

    @Test("removed background keys are rejected as unrecognized")
    func terminalBackgroundKeysRemoved() {
        // The terminal background is app-owned; the old customization keys
        // must fail loudly instead of silently doing nothing.
        let parsed = ConfigurationLoader.parse(#"""
        [terminal]
        background = "1e1e2e"
        background_opacity = 0.95
        """#)
        let messages = parsed.diagnostics.map(\.message)

        for key in ["background", "background_opacity"] {
            #expect(messages.contains {
                $0.contains("[terminal].\(key)") && $0.contains("not recognized")
            })
        }
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

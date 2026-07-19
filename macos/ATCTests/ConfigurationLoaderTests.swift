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
        [terminal]
        font_size = 14
        """#)

        #expect(parsed.diagnostics.contains {
            $0.severity == .error
                && $0.message.contains("[terminal]")
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
    }
}

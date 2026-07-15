import Testing
@testable import ATC

@Suite("Keyboard config parser")
struct KeyboardConfigParserTests {
    @Test("keyboard and keybinding scalars parse with comments")
    func happyPath() {
        let parsed = KeyboardConfigParser.parse(#"""
        # general comment
        [keyboard]
        leader = "cmd+k" # trailing comment
        leader_timeout_ms = 1800
        clear_default_keybindings = false

        [keybindings]
        "cmd+b" = "view.toggle-sidebar"
        "leader>b" = "view.toggle-sidebar"
        """#)
        #expect(parsed.diagnostics.isEmpty)
        #expect(parsed.tables["keyboard"]?.count == 3)
        #expect(parsed.tables["keybindings"]?.map(\.key) == ["cmd+b", "leader>b"])
    }

    @Test("source order, quoted and bare keys, and escapes are preserved")
    func orderAndEscapes() {
        let parsed = KeyboardConfigParser.parse(#"""
        [keyboard]
        leader = "cmd+\u006b"
        [keybindings]
        bare-key = "data.refresh"
        "cmd+\u0062" = "view.toggle-\u0073idebar"
        "cmd+#" = "data.refresh" # hash inside quotes
        """#)
        let entries = parsed.tables["keybindings", default: []]
        #expect(entries.map(\.key) == ["bare-key", "cmd+b", "cmd+#"])
        #expect(entries.map(\.line) == [4, 5, 6])
        #expect(entries[1].value == .string("view.toggle-sidebar"))
    }

    @Test("duplicate keys warn and retain ordered replacement entries")
    func duplicateKey() {
        let parsed = KeyboardConfigParser.parse(#"""
        [keybindings]
        "cmd+b" = "data.refresh"
        "cmd+b" = "view.toggle-sidebar"
        """#)
        #expect(parsed.tables["keybindings"]?.count == 2)
        #expect(parsed.diagnostics == [.init(
            severity: .warning,
            line: 3,
            message: "Duplicate key 'cmd+b' in [keybindings]; the later entry wins"
        )])
    }

    @Test("unsupported TOML constructs report their exact lines")
    func unsupportedConstructs() {
        let parsed = KeyboardConfigParser.parse(#"""
        [keyboard]
        array = [1]
        inline = { x = 1 }
        dotted.key = "x"
        float = 1.5
        literal = 'x'
        multiline = """x"""
        date = 2026-07-15
        """#)
        #expect(parsed.diagnostics.filter { $0.severity == .error }.map(\.line)
            == [2, 3, 4, 5, 6, 7, 8])
        let messages = parsed.diagnostics.map(\.message).joined(separator: " ")
        #expect(messages.contains("Arrays"))
        #expect(messages.contains("Inline tables"))
        #expect(messages.contains("Dotted keys"))
        #expect(messages.contains("Floats"))
        #expect(messages.contains("Literal strings"))
        #expect(messages.contains("Multiline strings"))
        #expect(messages.contains("Dates"))
    }

    @Test("unknown keyboard keys warn while unknown tables are silent")
    func unknownKeysAndTables() {
        let parsed = KeyboardConfigParser.parse(#"""
        [future]
        setting = ["a construct this parser otherwise rejects"]
        setting = "replacement"
        [keyboard]
        future_setting = true
        """#)
        #expect(parsed.tables["future"] == nil)
        #expect(parsed.diagnostics.count == 1)
        #expect(parsed.diagnostics[0].severity == .warning)
        #expect(parsed.diagnostics[0].line == 5)
        #expect(parsed.diagnostics[0].message.contains("future_setting"))
    }

    @Test("malformed lines report errors without derailing the table")
    func malformedLines() {
        let parsed = KeyboardConfigParser.parse(#"""
        top = "before any table"
        [keyboard]
        leader "cmd+k"
        leader = "cmd+k
        = "data.refresh"
        leader = "cmd+k"
        """#)
        #expect(parsed.diagnostics.filter { $0.severity == .error }.map(\.line)
            == [1, 3, 4, 5])
        #expect(parsed.tables["keyboard"]?.map(\.line) == [6])
    }

    @Test("empty text and blank comments produce an empty config")
    func emptyConfig() {
        #expect(KeyboardConfigParser.parse("").tables.isEmpty)
        #expect(KeyboardConfigParser.parse("\n # comment\n").diagnostics.isEmpty)
    }
}

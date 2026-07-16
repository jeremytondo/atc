import Testing
@testable import ATC

@Suite("Keyboard trigger parsing")
struct TriggerParsingTests {
    @Test("modifier aliases are case-insensitive and normalized")
    func modifierAliases() throws {
        let aliases = ["CMD+b", "Command+b", "SUPER+b"]
        for alias in aliases {
            let stroke = try #require(try? KeyStroke.parse(alias).get())
            #expect(stroke == KeyStroke(key: "b", modifiers: .command))
        }
        #expect(try KeyStroke.parse("Control+B").get()
            == KeyStroke(key: "b", modifiers: .control))
        #expect(try KeyStroke.parse("ALT+B").get()
            == KeyStroke(key: "b", modifiers: .option))
    }

    @Test("shift and multiple modifiers normalize independently of spelling")
    func multipleModifiers() throws {
        let stroke = try KeyStroke.parse(" shift + option + CMD + N ").get()
        #expect(stroke.key == "n")
        #expect(stroke.modifiers == [.command, .option, .shift])
        #expect(stroke.description == "cmd+opt+shift+n")
    }

    @Test("spoken shortcuts name modifiers in glyph order")
    func spokenDescription() {
        #expect(KeyStroke(
            key: "p",
            modifiers: [.control, .option, .shift, .command]
        ).spokenDescription == "Control Option Shift Command P")
        #expect(KeyStroke.escape.spokenDescription == "Escape")
    }

    @Test("sequence parsing reserves leader for step one")
    func sequences() throws {
        #expect(try ParsedKeySequence.parse("cmd+b").get()
            == .direct(KeyStroke(key: "b", modifiers: .command)))
        #expect(try ParsedKeySequence.parse(" leader > b ").get()
            == .leader(continuation: KeyStroke(key: "b", modifiers: [])))
        #expect(try ParsedKeySequence.parse("leader>cmd+shift+b").get()
            == .leader(continuation: KeyStroke(
                key: "b", modifiers: [.command, .shift]
            )))
    }

    @Test("invalid direct triggers are rejected with actionable tokens")
    func invalidDirectTriggers() {
        let invalid = [
            "b", "shift+b", "cmd+hyper+b", "cmd+cmd+b", "cmd+",
            "cmd+return", "cmd+escape", "cmd+f12",
        ]
        for trigger in invalid {
            guard case .failure(let error) = KeyStroke.parse(trigger) else {
                Issue.record("Expected \(trigger) to fail")
                continue
            }
            #expect(!error.message.isEmpty)
        }
    }

    @Test("unsupported sequence shapes are rejected")
    func invalidSequences() {
        let invalid = [
            "leader>a>b", "cmd+k>b", "x>leader", "leader>leader", "leader",
        ]
        for sequence in invalid {
            #expect(ParsedKeySequence.parse(sequence).isFailure)
        }
    }
}

private extension Result {
    var isFailure: Bool {
        if case .failure = self { true } else { false }
    }
}

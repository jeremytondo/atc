import AppKit
import Testing
@testable import ATC

@Suite("Keyboard event normalization")
struct EventNormalizationTests {
    @Test("layout-resolved command, shift, and option events normalize")
    func printableEvents() throws {
        let commandB = try #require(event(
            flags: .command,
            characters: "b",
            ignoringModifiers: "b",
            keyCode: 11
        ))
        #expect(KeyStroke.normalize(event: commandB)
            == KeyStroke(key: "b", modifiers: .command))

        let shiftedOne = try #require(event(
            flags: [.command, .shift],
            characters: "!",
            ignoringModifiers: "1",
            keyCode: 18
        ))
        #expect(KeyStroke.normalize(event: shiftedOne)
            == KeyStroke(key: "1", modifiers: [.command, .shift]))

        let optionB = try #require(event(
            flags: .option,
            characters: "∫",
            ignoringModifiers: "b",
            keyCode: 11
        ))
        #expect(KeyStroke.normalize(event: optionB)
            == KeyStroke(key: "b", modifiers: .option))
    }

    @Test("escape normalizes and function keys forward as unmappable")
    func specialEvents() throws {
        let escape = try #require(event(
            flags: [], characters: "\u{1b}", ignoringModifiers: "\u{1b}", keyCode: 53
        ))
        #expect(KeyStroke.normalize(event: escape) == .escape)

        let functionCharacter = String(UnicodeScalar(NSF1FunctionKey)!)
        let function = try #require(event(
            flags: [],
            characters: functionCharacter,
            ignoringModifiers: functionCharacter,
            keyCode: 122
        ))
        #expect(KeyStroke.normalize(event: function) == nil)
    }

    private func event(
        flags: NSEvent.ModifierFlags,
        characters: String,
        ignoringModifiers: String,
        keyCode: UInt16
    ) -> NSEvent? {
        NSEvent.keyEvent(
            with: .keyDown,
            location: .zero,
            modifierFlags: flags,
            timestamp: 0,
            windowNumber: 0,
            context: nil,
            characters: characters,
            charactersIgnoringModifiers: ignoringModifiers,
            isARepeat: false,
            keyCode: keyCode
        )
    }
}

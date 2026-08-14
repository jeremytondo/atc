import Testing

@testable import ATC

@Suite("Native shortcut classification")
struct NativeShortcutsTests {
    @Test("app-handled shortcuts map to native actions")
    func appHandled() throws {
        let expected: [(String, AppAction)] = [
            ("cmd+q", .terminate),
            ("cmd+w", .closeWindow),
            ("cmd+shift+w", .closeAllWindows),
        ]

        for (trigger, action) in expected {
            #expect(NativeShortcuts.appAction(for: try stroke(trigger)) == action)
        }
    }

    @Test("responder-owned and unrelated shortcuts have no app action")
    func passThrough() throws {
        for trigger in ["cmd+c", "cmd+v", "cmd+z", "cmd+a", "cmd+h", "cmd+m", "cmd+,"] {
            #expect(NativeShortcuts.appAction(for: try stroke(trigger)) == nil)
        }
        #expect(NativeShortcuts.appAction(for: try stroke("cmd+y")) == nil)
        #expect(
            NativeShortcuts.appAction(
                for: KeyStroke(key: "x", modifiers: [])
            ) == nil)
    }

    private func stroke(_ text: String) throws -> KeyStroke {
        try KeyStroke.parse(text).get()
    }
}

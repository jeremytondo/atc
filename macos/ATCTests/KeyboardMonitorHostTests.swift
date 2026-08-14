import AppKit
import Testing

@testable import ATC

@MainActor
@Suite("Keyboard monitor host")
struct KeyboardMonitorHostTests {
    @Test("native app shortcuts dispatch once and consume while other keys forward")
    func nativeDispatchAndForwarding() throws {
        _ = NSApplication.shared
        var actions: [AppAction] = []
        let router = WindowKeyboardRouter(keymap: try Keymap.resolve().get()) { _ in .available }
        router.isSuspended = { true }
        let coordinator = KeyboardMonitorHost.Coordinator(
            router: router,
            onDeactivate: {},
            focusFallback: {},
            nativeShortcutDispatch: NativeShortcutDispatch { action, _ in
                actions.append(action)
            }
        )
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 100, height: 100),
            styleMask: [.titled],
            backing: .buffered,
            defer: true
        )

        let quit = try #require(event(flags: .command, characters: "q", keyCode: 12))
        #expect(coordinator.handleKeyDown(event: quit, in: window) == nil)
        #expect(actions == [.terminate])

        let quitRepeat = try #require(
            event(
                flags: .command,
                characters: "q",
                keyCode: 12,
                isRepeat: true
            ))
        #expect(coordinator.handleKeyDown(event: quitRepeat, in: window) == nil)
        #expect(actions == [.terminate])

        let copy = try #require(event(flags: .command, characters: "c", keyCode: 8))
        #expect(coordinator.handleKeyDown(event: copy, in: window) === copy)

        let character = try #require(event(flags: [], characters: "x", keyCode: 7))
        #expect(coordinator.handleKeyDown(event: character, in: window) === character)
        #expect(actions == [.terminate])
    }

    // Normalization resolves the key through the event's keyCode and the
    // live layout, so each synthesized event must carry the real keyCode.
    private func event(
        flags: NSEvent.ModifierFlags,
        characters: String,
        keyCode: UInt16,
        isRepeat: Bool = false
    ) -> NSEvent? {
        NSEvent.keyEvent(
            with: .keyDown,
            location: .zero,
            modifierFlags: flags,
            timestamp: 0,
            windowNumber: 0,
            context: nil,
            characters: characters,
            charactersIgnoringModifiers: characters,
            isARepeat: isRepeat,
            keyCode: keyCode
        )
    }
}

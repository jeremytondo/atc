import AppKit
import Testing
@testable import ATC

@MainActor
@Suite("Window keyboard router")
struct KeyboardRouterTests {
    private func keymap(_ config: String = "", generation: Int = 1) throws -> ResolvedKeymap {
        try Keymap.resolve(
            user: ConfigurationLoader.parse(config),
            generation: generation
        ).get()
    }

    private func stroke(_ text: String) throws -> KeyStroke {
        try KeyStroke.parse(text).get()
    }

    @Test("a direct hit executes once and consumes repeats")
    func directAndRepeat() throws {
        var executions: [CommandID] = []
        let router = WindowKeyboardRouter(keymap: try keymap()) {
            executions.append($0)
            return .available
        }
        let refresh = try stroke("cmd+r")
        #expect(router.handle(refresh, isRepeat: false))
        #expect(router.handle(refresh, isRepeat: true))
        #expect(executions == [.refresh])
    }

    @Test("unrelated strokes and idle escape forward unchanged")
    func idleForwarding() throws {
        let router = WindowKeyboardRouter(keymap: try keymap()) { _ in .available }
        #expect(!router.handle(KeyStroke(key: "x", modifiers: []), isRepeat: false))
        #expect(!router.handle(.escape, isRepeat: false))
    }

    @Test("leader activation pends and a continuation executes")
    func leaderContinuation() throws {
        var executions: [CommandID] = []
        let router = WindowKeyboardRouter(keymap: try keymap()) {
            executions.append($0)
            return .available
        }
        #expect(router.handle(try stroke("cmd+k"), isRepeat: false))
        #expect(router.pendingNode != nil)
        #expect(router.handle(KeyStroke(key: "b", modifiers: []), isRepeat: false))
        #expect(router.pendingNode == nil)
        #expect(executions == [.toggleSidebar])
    }

    @Test("an inactive leader sequence times out silently and forwards the next key")
    func leaderTimeout() async throws {
        let router = WindowKeyboardRouter(keymap: try keymap()) { _ in .available }
        router.pendingTimeout = .milliseconds(1)

        #expect(router.handle(try stroke("cmd+k"), isRepeat: false))
        #expect(router.pendingNode != nil)
        let timeoutTask = try #require(router.pendingTimeoutTask)
        await timeoutTask.value

        #expect(router.pendingNode == nil)
        #expect(router.flash == nil)
        #expect(!router.handle(KeyStroke(key: "x", modifiers: []), isRepeat: false))
    }

    @Test("a continuation invalidates its pending timeout")
    func continuationInvalidatesTimeout() async throws {
        var executions: [CommandID] = []
        let router = WindowKeyboardRouter(keymap: try keymap()) {
            executions.append($0)
            return .available
        }

        #expect(router.handle(try stroke("cmd+k"), isRepeat: false))
        let timeoutTask = try #require(router.pendingTimeoutTask)
        #expect(router.handle(KeyStroke(key: "b", modifiers: []), isRepeat: false))
        await timeoutTask.value

        #expect(router.pendingNode == nil)
        #expect(router.pendingTimeoutTask == nil)
        #expect(executions == [.toggleSidebar])
    }

    @Test("cancel invalidates the pending timeout")
    func cancelInvalidatesTimeout() async throws {
        let router = WindowKeyboardRouter(keymap: try keymap()) { _ in .available }

        #expect(router.handle(try stroke("cmd+k"), isRepeat: false))
        let timeoutTask = try #require(router.pendingTimeoutTask)
        router.cancel()
        await timeoutTask.value

        #expect(router.pendingNode == nil)
        #expect(router.pendingTimeoutTask == nil)
        #expect(router.flash == nil)
    }

    @Test("modified continuations are never retried against root")
    func continuationDoesNotRetryRoot() throws {
        var executions: [CommandID] = []
        let map = try keymap(#"""
        [keyboard]
        clear_default_keybindings = true
        [keybindings]
        "cmd+shift+y" = "data.refresh"
        "leader>x" = "view.toggle-sidebar"
        """#)
        let router = WindowKeyboardRouter(keymap: map) {
            executions.append($0)
            return .available
        }
        #expect(router.handle(try stroke("cmd+k"), isRepeat: false))
        #expect(router.handle(try stroke("cmd+shift+y"), isRepeat: false))
        #expect(executions.isEmpty)
        #expect(router.flash?.message == "No matching command")
        #expect(router.pendingNode == nil)
    }

    @Test("an unknown continuation is consumed, flashes, and returns idle")
    func unknownContinuation() throws {
        let router = WindowKeyboardRouter(keymap: try keymap()) { _ in .available }
        #expect(router.handle(try stroke("cmd+k"), isRepeat: false))
        #expect(router.handle(KeyStroke(key: "x", modifiers: []), isRepeat: false))
        #expect(router.flash == RouterFlash(message: "No matching command"))
        #expect(router.pendingNode == nil)
    }

    @Test("a lingering flash clears when a new sequence starts")
    func flashClearsOnNewSequence() throws {
        let router = WindowKeyboardRouter(keymap: try keymap()) { _ in .available }
        #expect(router.handle(try stroke("cmd+k"), isRepeat: false))
        #expect(router.handle(KeyStroke(key: "x", modifiers: []), isRepeat: false))
        #expect(router.flash != nil)

        #expect(router.handle(try stroke("cmd+k"), isRepeat: false))
        #expect(router.flash == nil)
        #expect(router.pendingNode != nil)
    }

    @Test("escape and focus loss cancel pending sequences silently")
    func cancellation() throws {
        let router = WindowKeyboardRouter(keymap: try keymap()) { _ in .available }
        #expect(router.handle(try stroke("cmd+k"), isRepeat: false))
        #expect(router.handle(.escape, isRepeat: false))
        #expect(router.pendingNode == nil)
        #expect(router.flash == nil)

        #expect(router.handle(try stroke("cmd+k"), isRepeat: false))
        router.cancel()
        #expect(router.pendingNode == nil)
        #expect(router.flash == nil)
    }

    @Test("unavailable commands consume and surface their reason")
    func unavailableCommand() throws {
        var executions = 0
        let reason = "Requires a configured Connection"
        let router = WindowKeyboardRouter(keymap: try keymap()) { _ in
            executions += 1
            return .unavailable(reason: reason)
        }
        #expect(router.handle(try stroke("cmd+b"), isRepeat: false))
        #expect(executions == 1)
        #expect(router.flash == RouterFlash(message: reason))
    }

    @Test("replacing a keymap cancels pending before using the new tree")
    func replacementCancelsPending() throws {
        let router = WindowKeyboardRouter(
            keymap: try keymap(generation: 1)
        ) { _ in .available }
        #expect(router.handle(try stroke("cmd+k"), isRepeat: false))
        router.keymap = try keymap(generation: 2)
        #expect(router.pendingNode == nil)
    }

    @Test("a configured leader with no surviving sequences forwards")
    func unreservedLeader() throws {
        let map = try keymap(#"""
        [keyboard]
        clear_default_keybindings = true
        leader = "ctrl+j"
        """#)
        let router = WindowKeyboardRouter(keymap: map) { _ in .available }
        #expect(!router.handle(try stroke("ctrl+j"), isRepeat: false))
    }

    @Test("suspension forwards registered bindings until routing resumes")
    func suspension() throws {
        // Reference box: the router's isSuspended closure is implicitly
        // Sendable (@MainActor), so it cannot capture a mutable local.
        final class Flag { var value = true }
        let suspended = Flag()
        var executions: [CommandID] = []
        let router = WindowKeyboardRouter(keymap: try keymap()) {
            executions.append($0)
            return .available
        }
        router.isSuspended = { suspended.value }
        let refresh = try stroke("cmd+r")

        #expect(!router.handle(refresh, isRepeat: false))
        #expect(executions.isEmpty)
        suspended.value = false
        #expect(router.handle(refresh, isRepeat: false))
        #expect(executions == [.refresh])
    }

    @Test("exact ⌘digit and ⌥⌘digit dispatch sidebar jumps and consume")
    func sidebarJumpDispatch() throws {
        var jumps: [SidebarJump] = []
        let router = WindowKeyboardRouter(keymap: try keymap()) { _ in .available }
        router.performJump = { jumps.append($0) }

        #expect(router.handle(KeyStroke(key: "1", modifiers: [.command]), isRepeat: false))
        #expect(router.handle(KeyStroke(key: "3", modifiers: [.command, .option]), isRepeat: false))
        #expect(jumps == [.thread(slot: 0), .terminal(slot: 2)])

        // Repeats are consumed without dispatching again.
        #expect(router.handle(KeyStroke(key: "1", modifiers: [.command]), isRepeat: true))
        #expect(jumps.count == 2)
    }

    @Test("non-exact modifier combos are not jumps")
    func sidebarJumpExactModifiers() throws {
        var jumps: [SidebarJump] = []
        let router = WindowKeyboardRouter(keymap: try keymap()) { _ in .available }
        router.performJump = { jumps.append($0) }

        #expect(!router.handle(KeyStroke(key: "1", modifiers: [.command, .shift]), isRepeat: false))
        #expect(!router.handle(KeyStroke(key: "1", modifiers: [.option]), isRepeat: false))
        #expect(!router.handle(KeyStroke(key: "1", modifiers: []), isRepeat: false))
        #expect(jumps.isEmpty)
    }

    @Test("suspension forwards jump shortcuts")
    func sidebarJumpSuspension() throws {
        var jumps: [SidebarJump] = []
        let router = WindowKeyboardRouter(keymap: try keymap()) { _ in .available }
        router.performJump = { jumps.append($0) }
        router.isSuspended = { true }

        #expect(!router.handle(KeyStroke(key: "1", modifiers: [.command]), isRepeat: false))
        #expect(jumps.isEmpty)
    }

    @Test("a pending sequence keeps sequence semantics for ⌘digit")
    func sidebarJumpDuringPendingSequence() throws {
        var jumps: [SidebarJump] = []
        let router = WindowKeyboardRouter(keymap: try keymap()) { _ in .available }
        router.performJump = { jumps.append($0) }

        #expect(router.handle(try stroke("cmd+k"), isRepeat: false))
        #expect(router.handle(KeyStroke(key: "1", modifiers: [.command]), isRepeat: false))
        #expect(jumps.isEmpty)
        #expect(router.flash == RouterFlash(message: "No matching command"))
        #expect(router.pendingNode == nil)
    }

    @Test("window resign and app deactivation clear held modifiers")
    func heldModifiersResetOnResign() throws {
        _ = NSApplication.shared
        let router = WindowKeyboardRouter(keymap: try keymap()) { _ in .available }
        let coordinator = KeyboardMonitorHost.Coordinator(
            router: router,
            onDeactivate: {},
            focusFallback: {}
        )
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 100, height: 100),
            styleMask: [.titled],
            backing: .buffered,
            defer: true
        )
        window.isReleasedWhenClosed = false
        defer { coordinator.detach() }
        coordinator.attach(to: window)

        router.heldModifiers = [.command]
        NotificationCenter.default.post(name: NSWindow.didResignKeyNotification, object: window)
        #expect(router.heldModifiers == [])

        router.heldModifiers = [.command, .option]
        NotificationCenter.default.post(
            name: NSApplication.didResignActiveNotification,
            object: NSApp
        )
        #expect(router.heldModifiers == [])

        // Becoming key reseeds from the live hardware state — no modifiers
        // are physically held in a test run, so a stale value clears.
        router.heldModifiers = [.command]
        NotificationCenter.default.post(name: NSWindow.didBecomeKeyNotification, object: window)
        #expect(router.heldModifiers == KeyStroke.Modifiers(NSEvent.modifierFlags))
    }

    @Test("external unavailable feedback uses the router flash lifecycle")
    func showUnavailable() async throws {
        let router = WindowKeyboardRouter(keymap: try keymap()) { _ in .available }
        router.showUnavailable(reason: "Unavailable now")
        #expect(router.flash == RouterFlash(message: "Unavailable now"))
        // Settling releases the main actor so the router's clearing task can
        // run; a run-loop pump would hold the actor and dead-lock the clear.
        await settle(until: { router.flash == nil })
        #expect(router.flash == nil)
    }
}

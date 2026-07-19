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

    @Test("only a timeout for the current generation cancels")
    func timeoutGeneration() throws {
        let router = WindowKeyboardRouter(
            keymap: try keymap(generation: 7)
        ) { _ in .available }
        #expect(router.handle(try stroke("cmd+k"), isRepeat: false))
        router.handleTimeout(generation: 6)
        #expect(router.pendingNode != nil)
        router.handleTimeout(generation: 7)
        #expect(router.pendingNode == nil)
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
        var isSuspended = true
        var executions: [CommandID] = []
        let router = WindowKeyboardRouter(keymap: try keymap()) {
            executions.append($0)
            return .available
        }
        router.isSuspended = { isSuspended }
        let refresh = try stroke("cmd+r")

        #expect(!router.handle(refresh, isRepeat: false))
        #expect(executions.isEmpty)
        isSuspended = false
        #expect(router.handle(refresh, isRepeat: false))
        #expect(executions == [.refresh])
    }

    @Test("external unavailable feedback uses the router flash lifecycle")
    func showUnavailable() async throws {
        let router = WindowKeyboardRouter(keymap: try keymap()) { _ in .available }
        router.showUnavailable(reason: "Unavailable now")
        #expect(router.flash == RouterFlash(message: "Unavailable now"))
        // Awaiting releases the main actor so the router's clearing task can
        // run; a run-loop pump would hold the actor and dead-lock the clear.
        for _ in 0..<100 where router.flash != nil {
            try await Task.sleep(for: .milliseconds(50))
        }
        #expect(router.flash == nil)
    }
}

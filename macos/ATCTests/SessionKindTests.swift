import Foundation
import Testing
import ATCAPI
@testable import ATC

/// Session classification (agent vs terminal) and the display-name
/// fallback rules — a pure join against the action registry.
@Suite("SessionKind")
struct SessionKindTests {
    private let actions: [ATCAction] = [
        ATCAction(name: "claude", type: "agent", origin: "builtin", enabled: true, label: "Claude"),
        ATCAction(name: "lazygit", origin: "custom", enabled: true, label: "LazyGit"),
        ATCAction(name: "untyped", origin: "custom", enabled: true),
    ]

    private func session(name: String? = nil, action: String?) -> Session {
        Session(
            id: "ses_x", name: name, action: action, environment: "host",
            workingDir: "/home/dev", status: .running, attachable: true,
            createdAt: .now, updatedAt: .now
        )
    }

    @Test("nil action is the Interactive Shell — a Terminal")
    func shellIsTerminal() {
        #expect(SessionKind.classify(session: session(action: nil), actions: actions) == .terminal)
    }

    @Test("an agent action classifies as a Session")
    func agentIsAgent() {
        #expect(SessionKind.classify(session: session(action: "claude"), actions: actions) == .agent)
    }

    @Test("a general action classifies as a Terminal, including untyped ones")
    func generalIsTerminal() {
        #expect(SessionKind.classify(session: session(action: "lazygit"), actions: actions) == .terminal)
        #expect(SessionKind.classify(session: session(action: "untyped"), actions: actions) == .terminal)
    }

    @Test("an action that no longer resolves falls back to Terminal")
    func unresolvedIsTerminal() {
        #expect(SessionKind.classify(session: session(action: "ghost"), actions: actions) == .terminal)
    }

    @Test("a user-given name always wins")
    func nameWins() {
        #expect(SessionKind.displayName(
            session: session(name: "Fix parser", action: "claude"), actions: actions
        ) == "Fix parser")
    }

    @Test("an unnamed agent session shows its action label")
    func unnamedAgentShowsLabel() {
        #expect(SessionKind.displayName(session: session(action: "claude"), actions: actions) == "Claude")
    }

    @Test("an unnamed interactive shell shows Terminal")
    func unnamedShellShowsTerminal() {
        #expect(SessionKind.displayName(session: session(action: nil), actions: actions) == "Terminal")
        #expect(SessionKind.actionLabel(session: session(action: nil), actions: actions) == "Terminal")
    }

    @Test("an unnamed action terminal shows its action label")
    func unnamedActionTerminalShowsLabel() {
        #expect(SessionKind.displayName(session: session(action: "lazygit"), actions: actions) == "LazyGit")
    }

    @Test("an unresolvable action falls back to its raw name")
    func unresolvedShowsRawName() {
        #expect(SessionKind.displayName(session: session(action: "ghost"), actions: actions) == "ghost")
        #expect(SessionKind.actionLabel(session: session(action: "ghost"), actions: actions) == "ghost")
    }

    @Test("an empty name falls back like a missing one")
    func emptyNameFallsBack() {
        #expect(SessionKind.displayName(
            session: session(name: "", action: "claude"), actions: actions
        ) == "Claude")
    }
}

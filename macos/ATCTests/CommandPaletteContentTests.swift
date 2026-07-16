import Foundation
import Testing
@testable import ATC

@MainActor
@Suite("Command palette content")
struct CommandPaletteContentTests {
    @Test("rows use stable alphabetical order for empty and filtered queries")
    func stableOrdering() throws {
        let fixture = makeFixture()
        #expect(rows(query: "", fixture: fixture).map(\.title) == [
            "New Project…", "New Session", "New Terminal", "New Workspace…",
            "Refresh", "Reload Configuration", "Toggle Sidebar",
        ])
        #expect(rows(query: "new", fixture: fixture).map(\.title) == [
            "New Project…", "New Session", "New Terminal", "New Workspace…",
        ])
        #expect(CommandPaletteContent.isOrderedBefore(
            title: "Same", id: .newProject,
            thanTitle: "same", id: .newSession
        ))
        #expect(!CommandPaletteContent.isOrderedBefore(
            title: "same", id: .newSession,
            thanTitle: "Same", id: .newProject
        ))
    }

    @Test("nonmatching commands are excluded")
    func filtering() throws {
        let fixture = makeFixture()
        #expect(rows(query: "sidebar", fixture: fixture).map(\.id) == [.toggleSidebar])
        #expect(rows(query: "missing", fixture: fixture).isEmpty)
    }

    @Test("shortcuts follow direct rebinds and unbinds")
    func directShortcuts() throws {
        let rebound = makeFixture(config: #"""
        [keybindings]
        "ctrl+shift+r" = "data.refresh"
        """#)
        let reboundShortcut = try stroke("ctrl+shift+r")
        #expect(row(.refresh, fixture: rebound).shortcut == reboundShortcut)

        let unbound = makeFixture(config: #"""
        [keybindings]
        "cmd+r" = "unbind"
        """#)
        #expect(row(.refresh, fixture: unbound).shortcut == nil)
    }

    @Test("a command bound only through a Command Sequence has no shortcut")
    func sequenceIsNotAShortcut() throws {
        let fixture = makeFixture(config: #"""
        [keyboard]
        clear_default_keybindings = true
        [keybindings]
        "leader>x" = "project.new"
        """#)
        #expect(row(.newProject, fixture: fixture).shortcut == nil)
    }

    @Test("availability is evaluated from the current command context")
    func availability() throws {
        let fixture = makeFixture()
        #expect(row(.refresh, fixture: fixture).availability == .available)
        #expect(row(.newSession, fixture: fixture).availability == .unavailable(
            reason: "Requires an open Workspace on a reachable Connection"
        ))
    }

    private func makeFixture(config: String = "") -> Fixture {
        let suite = "CommandPaletteContentTests.\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        defaults.removePersistentDomain(forName: suite)
        let model = AppModel(
            connections: ConnectionsStore(
                defaults: defaults,
                credentials: InMemoryCredentialStore()
            ),
            clientFactory: { _ in MockATCClient() },
            terminalRecoveryMonitor: .disabled()
        )
        let state = WindowState.ephemeral()
        let store = KeyboardConfigStore(
            configURL: FileManager.default.temporaryDirectory
                .appending(path: UUID().uuidString)
        )
        let keymap = try! Keymap.resolve(
            user: KeyboardConfigParser.parse(config)
        ).get()
        return Fixture(
            keymap: keymap,
            context: CommandContext(
                appModel: model,
                windowState: state,
                configStore: store
            )
        )
    }

    private func rows(query: String, fixture: Fixture) -> [CommandPaletteRow] {
        CommandPaletteContent.rows(
            query: query,
            keymap: fixture.keymap,
            context: fixture.context
        )
    }

    private func row(_ id: CommandID, fixture: Fixture) -> CommandPaletteRow {
        try! #require(rows(query: "", fixture: fixture).first { $0.id == id })
    }

    private func stroke(_ text: String) throws -> KeyStroke {
        try KeyStroke.parse(text).get()
    }

    private struct Fixture {
        let keymap: ResolvedKeymap
        let context: CommandContext
    }
}

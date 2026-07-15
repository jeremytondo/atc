import Foundation
import Testing
@testable import ATC

@MainActor
@Suite("Keyboard config store")
struct KeyboardConfigStoreTests {
    private func fixture() throws -> (directory: URL, config: URL) {
        let directory = FileManager.default.temporaryDirectory
            .appending(path: "KeyboardConfigStoreTests-\(UUID().uuidString)")
        try FileManager.default.createDirectory(
            at: directory,
            withIntermediateDirectories: true
        )
        return (directory, directory.appending(path: "config.toml"))
    }

    private func write(_ text: String, to url: URL) throws {
        try Data(text.utf8).write(to: url, options: .atomic)
    }

    @Test("launch with a missing file silently resolves compiled defaults")
    func missingLaunch() throws {
        let fixture = try fixture()
        defer { try? FileManager.default.removeItem(at: fixture.directory) }
        let store = KeyboardConfigStore(configURL: fixture.config)
        store.loadAtLaunch()
        #expect(store.keymap.generation == 1)
        #expect(store.notice == nil)
        #expect(store.diagnostics.isEmpty)
        #expect(store.keymap.menuShortcuts[.toggleSidebar]
            == KeyStroke(key: "b", modifiers: .command))
    }

    @Test("launch with a valid file publishes its complete keymap")
    func validLaunch() throws {
        let fixture = try fixture()
        defer { try? FileManager.default.removeItem(at: fixture.directory) }
        try write(#"""
        [keyboard]
        clear_default_keybindings = true
        [keybindings]
        "ctrl+b" = "view.toggle-sidebar"
        """#, to: fixture.config)
        let store = KeyboardConfigStore(configURL: fixture.config)
        store.loadAtLaunch()
        #expect(store.keymap.generation == 1)
        #expect(store.keymap.menuShortcuts[.toggleSidebar]
            == KeyStroke(key: "b", modifiers: .control))
        #expect(store.notice == nil)
    }

    @Test("invalid launch config retains compiled defaults and one notice")
    func invalidLaunch() throws {
        let fixture = try fixture()
        defer { try? FileManager.default.removeItem(at: fixture.directory) }
        try write(#"""
        [keybindings]
        "cmd+c" = "data.refresh"
        "cmd+shift+x" = "unknown.command"
        """#, to: fixture.config)
        let store = KeyboardConfigStore(configURL: fixture.config)
        store.loadAtLaunch()
        #expect(store.keymap.generation == 0)
        #expect(store.keymap.menuShortcuts[.toggleSidebar]
            == KeyStroke(key: "b", modifiers: .command))
        #expect(store.notice?.message.contains("using default keybindings") == true)
        #expect(store.diagnostics.count { $0.severity == .error } == 2)
    }

    @Test("successful reload swaps atomically, bumps generation, and clears notice")
    func successfulReload() throws {
        let fixture = try fixture()
        defer { try? FileManager.default.removeItem(at: fixture.directory) }
        let store = KeyboardConfigStore(configURL: fixture.config)
        store.loadAtLaunch()
        try write(#"""
        [keyboard]
        clear_default_keybindings = true
        [keybindings]
        "ctrl+r" = "data.refresh"
        """#, to: fixture.config)
        store.reload()
        #expect(store.keymap.generation == 2)
        #expect(store.keymap.menuShortcuts[.refresh]
            == KeyStroke(key: "r", modifiers: .control))
        #expect(store.notice == nil)
    }

    @Test("failed reload retains the previous generation and exposes diagnostics once")
    func failedReload() throws {
        let fixture = try fixture()
        defer { try? FileManager.default.removeItem(at: fixture.directory) }
        try write(#"""
        [keybindings]
        "ctrl+r" = "data.refresh"
        """#, to: fixture.config)
        let store = KeyboardConfigStore(configURL: fixture.config)
        store.loadAtLaunch()
        let previousGeneration = store.keymap.generation
        let previousShortcut = store.keymap.menuShortcuts[.refresh]

        try write(#"""
        [keyboard]
        leader_timeout_ms = 0
        mystery = true
        """#, to: fixture.config)
        store.reload()
        #expect(store.keymap.generation == previousGeneration)
        #expect(store.keymap.menuShortcuts[.refresh] == previousShortcut)
        #expect(store.notice?.message.contains("keeping the previous keybindings") == true)
        #expect(store.diagnostics.contains { $0.severity == .error && $0.line == 2 })
        #expect(store.diagnostics.contains { $0.severity == .warning && $0.line == 3 })

        store.dismissNotice()
        #expect(store.notice == nil)
    }

    @Test("configuration location honors non-empty XDG_CONFIG_HOME")
    func configLocation() {
        let home = URL(fileURLWithPath: "/tmp/example-home")
        #expect(KeyboardConfigStore.defaultConfigURL(
            environment: ["XDG_CONFIG_HOME": "/tmp/xdg"],
            homeDirectory: home
        ).path == "/tmp/xdg/atc/config.toml")
        #expect(KeyboardConfigStore.defaultConfigURL(
            environment: ["XDG_CONFIG_HOME": "  "],
            homeDirectory: home
        ).path == "/tmp/example-home/.config/atc/config.toml")
    }
}

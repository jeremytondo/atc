import Foundation
import Testing
@testable import ATC

@MainActor
@Suite("Configuration store")
struct ConfigurationStoreTests {
    private func fixture() throws -> (directory: URL, config: URL) {
        let directory = FileManager.default.temporaryDirectory
            .appending(path: "ConfigurationStoreTests-\(UUID().uuidString)")
        try FileManager.default.createDirectory(
            at: directory,
            withIntermediateDirectories: true
        )
        return (directory, directory.appending(path: "macos.toml"))
    }

    private func write(_ text: String, to url: URL) throws {
        try Data(text.utf8).write(to: url, options: .atomic)
    }

    @Test("launch with a missing file silently resolves compiled defaults")
    func missingLaunch() throws {
        let fixture = try fixture()
        defer { try? FileManager.default.removeItem(at: fixture.directory) }
        let store = ConfigurationStore(configURL: fixture.config)

        store.loadAtLaunch()

        #expect(store.configuration.keymap.generation == 1)
        #expect(store.notice == nil)
        #expect(store.diagnostics.isEmpty)
        #expect(store.configuration.keymap.menuShortcuts[.toggleSidebar]
            == KeyStroke(key: "b", modifiers: .command))
    }

    @Test("launch with a valid file publishes its complete configuration")
    func validLaunch() throws {
        let fixture = try fixture()
        defer { try? FileManager.default.removeItem(at: fixture.directory) }
        try write(#"""
        [keyboard]
        clear_default_keybindings = true
        [keybindings]
        "ctrl+b" = "view.toggle-sidebar"
        """#, to: fixture.config)
        let store = ConfigurationStore(configURL: fixture.config)

        store.loadAtLaunch()

        #expect(store.configuration.keymap.generation == 1)
        #expect(store.configuration.keymap.menuShortcuts[.toggleSidebar]
            == KeyStroke(key: "b", modifiers: .control))
        #expect(store.notice == nil)
    }

    @Test("invalid launch config retains compiled defaults and reports the first error")
    func invalidLaunch() throws {
        let fixture = try fixture()
        defer { try? FileManager.default.removeItem(at: fixture.directory) }
        try write(#"""
        [keybindings]
        "cmd+c" = "data.refresh"
        "cmd+shift+x" = "unknown.command"
        """#, to: fixture.config)
        let store = ConfigurationStore(configURL: fixture.config)

        store.loadAtLaunch()

        #expect(store.configuration.keymap.generation == 0)
        #expect(store.configuration.keymap.menuShortcuts[.toggleSidebar]
            == KeyStroke(key: "b", modifiers: .command))
        #expect(store.notice?.message.contains("using defaults") == true)
        #expect(store.notice?.message.contains("First error:") == true)
        #expect(store.diagnostics.count { $0.severity == .error } == 2)
    }

    @Test("successful reload swaps atomically, bumps generation, and clears notice")
    func successfulReload() throws {
        let fixture = try fixture()
        defer { try? FileManager.default.removeItem(at: fixture.directory) }
        let store = ConfigurationStore(configURL: fixture.config)
        store.loadAtLaunch()
        try write(#"""
        [keyboard]
        mystery = true
        """#, to: fixture.config)
        store.reload()
        #expect(store.notice != nil)

        try write(#"""
        [keyboard]
        clear_default_keybindings = true
        [keybindings]
        "ctrl+r" = "data.refresh"
        """#, to: fixture.config)

        store.reload()

        #expect(store.configuration.keymap.generation == 2)
        #expect(store.configuration.keymap.menuShortcuts[.refresh]
            == KeyStroke(key: "r", modifiers: .control))
        #expect(store.notice == nil)
    }

    @Test("failed reload retains the complete previous configuration")
    func failedReload() throws {
        let fixture = try fixture()
        defer { try? FileManager.default.removeItem(at: fixture.directory) }
        try write(#"""
        [keybindings]
        "ctrl+r" = "data.refresh"
        """#, to: fixture.config)
        let store = ConfigurationStore(configURL: fixture.config)
        store.loadAtLaunch()
        let previousGeneration = store.configuration.keymap.generation
        let previousShortcut = store.configuration.keymap.menuShortcuts[.refresh]

        try write(#"""
        [keyboard]
        leader_timeout_ms = 0
        mystery = true
        """#, to: fixture.config)
        store.reload()

        #expect(store.configuration.keymap.generation == previousGeneration)
        #expect(store.configuration.keymap.menuShortcuts[.refresh] == previousShortcut)
        #expect(store.notice?.message.contains("keeping the previous configuration") == true)
        #expect(store.notice?.message.contains("[keyboard]") == true)
        #expect(store.diagnostics.contains {
            $0.severity == .error && $0.message.contains("[keyboard].leader_timeout_ms")
        })
        #expect(store.diagnostics.contains {
            $0.severity == .error && $0.message.contains("[keyboard].mystery")
        })

        store.dismissNotice()
        #expect(store.notice == nil)
    }

    @Test("deleting the file and reloading resets the whole configuration")
    func deletedReload() throws {
        let fixture = try fixture()
        defer { try? FileManager.default.removeItem(at: fixture.directory) }
        try write(#"""
        [keyboard]
        clear_default_keybindings = true
        [keybindings]
        "ctrl+b" = "view.toggle-sidebar"
        """#, to: fixture.config)
        let store = ConfigurationStore(configURL: fixture.config)
        store.loadAtLaunch()
        try FileManager.default.removeItem(at: fixture.config)

        store.reload()

        #expect(store.configuration.keymap.generation == 2)
        #expect(store.configuration.keymap.menuShortcuts[.toggleSidebar]
            == KeyStroke(key: "b", modifiers: .command))
        #expect(store.notice == nil)
        #expect(store.diagnostics.isEmpty)
    }

    @Test("invalid terminal value rejects keyboard changes and preserves the prior configuration")
    func terminalFailureIsTransactional() throws {
        let fixture = try fixture()
        defer { try? FileManager.default.removeItem(at: fixture.directory) }
        try write(#"""
        [terminal]
        font_family = "Berkeley Mono"
        [keybindings]
        "ctrl+r" = "data.refresh"
        """#, to: fixture.config)
        var applied: [TerminalPreferences] = []
        let store = ConfigurationStore(
            configURL: fixture.config,
            onTerminalPreferencesApplied: { applied.append($0) }
        )
        store.loadAtLaunch()
        let previous = store.configuration

        try write(#"""
        [keyboard]
        clear_default_keybindings = true
        [keybindings]
        "ctrl+b" = "view.toggle-sidebar"
        [terminal]
        padding_x = -1
        """#, to: fixture.config)
        store.reload()

        #expect(store.configuration.keymap.generation == previous.keymap.generation)
        #expect(store.configuration.keymap.menuShortcuts[.toggleSidebar]
            == previous.keymap.menuShortcuts[.toggleSidebar])
        #expect(store.configuration.terminal == previous.terminal)
        #expect(applied == [previous.terminal])
        #expect(store.notice?.message.contains("[terminal].padding_x") == true)
        #expect(store.notice?.message.contains("keeping the previous configuration") == true)
    }

    @Test("successful terminal reload and file deletion both invoke live apply")
    func terminalPreferencesApplyAndReset() throws {
        let fixture = try fixture()
        defer { try? FileManager.default.removeItem(at: fixture.directory) }
        try write(#"""
        [terminal]
        padding_x = 4
        """#, to: fixture.config)
        var applied: [TerminalPreferences] = []
        let store = ConfigurationStore(
            configURL: fixture.config,
            onTerminalPreferencesApplied: { applied.append($0) }
        )

        store.loadAtLaunch()
        try write(#"""
        [terminal]
        padding_x = 8
        """#, to: fixture.config)
        store.reload()
        try FileManager.default.removeItem(at: fixture.config)
        store.reload()

        #expect(applied.map(\.paddingX) == [4, 8, nil])
        #expect(store.configuration.terminal == TerminalPreferences())
    }

    @Test("invalid launch still applies default terminal preferences")
    func invalidLaunchAppliesDefaultTerminal() throws {
        let fixture = try fixture()
        defer { try? FileManager.default.removeItem(at: fixture.directory) }
        try write(#"""
        [terminal]
        theme = "Definitely Not A Real Theme"
        """#, to: fixture.config)
        var applied: [TerminalPreferences] = []
        let store = ConfigurationStore(
            configURL: fixture.config,
            onTerminalPreferencesApplied: { applied.append($0) }
        )
        store.loadAtLaunch()

        #expect(applied == [TerminalPreferences()])
        #expect(store.notice?.message.contains("using defaults") == true)
    }

    @Test("configuration location honors non-empty XDG_CONFIG_HOME")
    func configLocation() {
        let home = URL(fileURLWithPath: "/tmp/example-home")
        #expect(ConfigurationStore.defaultConfigURL(
            environment: ["XDG_CONFIG_HOME": "/tmp/xdg"],
            homeDirectory: home
        ).path == "/tmp/xdg/atc/macos.toml")
        #expect(ConfigurationStore.defaultConfigURL(
            environment: ["XDG_CONFIG_HOME": "  "],
            homeDirectory: home
        ).path == "/tmp/example-home/.config/atc/macos.toml")
    }
}

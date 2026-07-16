import Foundation
import Testing
@testable import ATC

@Suite("Keyboard keymap resolution")
struct KeymapResolutionTests {
    private func resolve(_ config: String = "", generation: Int = 1) throws -> ResolvedKeymap {
        try Keymap.resolve(
            user: KeyboardConfigParser.parse(config),
            generation: generation
        ).get()
    }

    private func stroke(_ text: String) throws -> KeyStroke {
        try KeyStroke.parse(text).get()
    }

    @Test("compiled defaults build the specified direct and leader tree")
    func defaults() throws {
        let keymap = try resolve()
        #expect(keymap.generation == 1)
        #expect(keymap.leaderTimeout == .milliseconds(1_800))
        #expect(command(at: try stroke("cmd+b"), in: keymap) == .toggleSidebar)
        #expect(command(at: try stroke("cmd+shift+p"), in: keymap)
            == .toggleCommandPalette)
        let paletteShortcut = try stroke("cmd+shift+p")
        #expect(keymap.menuShortcuts[.toggleCommandPalette]
            == paletteShortcut)
        #expect(command(at: try stroke("cmd+n"), in: keymap) == .newSession)
        #expect(command(at: try stroke("cmd+r"), in: keymap) == .refresh)
        #expect(command(at: try stroke("cmd+t"), in: keymap) == .newTerminal)
        #expect(command(at: try stroke("cmd+shift+n"), in: keymap) == .newWorkspace)

        let leader = try #require(prefix(at: try stroke("cmd+k"), in: keymap))
        #expect(leader.count == 3)
        #expect(command(in: leader[KeyStroke(key: "b", modifiers: [])]) == .toggleSidebar)
        #expect(command(in: leader[KeyStroke(key: "n", modifiers: [])]) == .newSession)
        #expect(command(in: leader[KeyStroke(key: "r", modifiers: [])]) == .refresh)
    }

    @Test("the palette binding can be rebound and unbound")
    func paletteBindingLayering() throws {
        let rebound = try resolve(#"""
        [keybindings]
        "ctrl+shift+p" = "view.toggle-command-palette"
        """#)
        #expect(command(at: try stroke("ctrl+shift+p"), in: rebound)
            == .toggleCommandPalette)
        let reboundShortcut = try stroke("ctrl+shift+p")
        #expect(rebound.menuShortcuts[.toggleCommandPalette]
            == reboundShortcut)

        let unbound = try resolve(#"""
        [keybindings]
        "cmd+shift+p" = "unbind"
        """#)
        #expect(unbound.root[try stroke("cmd+shift+p")] == nil)
        #expect(unbound.menuShortcuts[.toggleCommandPalette] == nil)
    }

    @Test("user entries replace, unbind, and can rebind defaults")
    func layering() throws {
        let replaced = try resolve(#"""
        [keybindings]
        "cmd+b" = "data.refresh"
        "cmd+n" = "unbind"
        "cmd+n" = "terminal.new"
        """#)
        #expect(command(at: try stroke("cmd+b"), in: replaced) == .refresh)
        #expect(command(at: try stroke("cmd+n"), in: replaced) == .newTerminal)

        let unbound = try resolve(#"""
        [keybindings]
        "cmd+b" = "unbind"
        """#)
        #expect(unbound.root[try stroke("cmd+b")] == nil)
    }

    @Test("clearing defaults leaves no leader reservation without sequences")
    func clearDefaults() throws {
        let keymap = try resolve(#"""
        [keyboard]
        clear_default_keybindings = true
        """#)
        #expect(keymap.root.isEmpty)
        #expect(keymap.menuShortcuts.isEmpty)
        #expect(keymap.root[try stroke("cmd+k")] == nil)
    }

    @Test("custom leaders expand all symbolic sequences")
    func customLeader() throws {
        let keymap = try resolve(#"""
        [keyboard]
        leader = "ctrl+j"
        [keybindings]
        "leader>x" = "project.new"
        """#)
        let node = try #require(prefix(at: try stroke("ctrl+j"), in: keymap))
        #expect(command(in: node[KeyStroke(key: "x", modifiers: [])]) == .newProject)
        #expect(keymap.root[try stroke("cmd+k")] == nil)
    }

    @Test("direct and expanded-prefix conflicts invalidate the candidate")
    func prefixConflicts() throws {
        let directConflict = Keymap.resolve(user: KeyboardConfigParser.parse(#"""
        [keybindings]
        "cmd+k" = "data.refresh"
        """#))
        let directDiagnostics = try failure(directConflict)
        #expect(directDiagnostics.contains {
            $0.message.contains("cmd+k") && $0.message.contains("both")
        })

        let expandedConflict = Keymap.resolve(user: KeyboardConfigParser.parse(#"""
        [keyboard]
        leader = "cmd+b"
        """#))
        let expandedDiagnostics = try failure(expandedConflict)
        #expect(expandedDiagnostics.contains {
            $0.message.contains("cmd+b") && $0.message.contains("unbind")
        })
    }

    @Test("protected shortcuts and leaders name the native command")
    func protectedShortcuts() throws {
        let direct = Keymap.resolve(user: KeyboardConfigParser.parse(#"""
        [keyboard]
        clear_default_keybindings = true
        [keybindings]
        "cmd+c" = "data.refresh"
        """#))
        #expect(try failure(direct).contains {
            $0.message.contains("cmd+c") && $0.message.contains("Copy")
        })

        let leader = Keymap.resolve(user: KeyboardConfigParser.parse(#"""
        [keyboard]
        clear_default_keybindings = true
        leader = "cmd+q"
        """#))
        #expect(try failure(leader).contains {
            $0.message.contains("cmd+q") && $0.message.contains("Quit")
        })
    }

    @Test("unknown command ids invalidate the entire candidate")
    func unknownCommand() throws {
        let result = Keymap.resolve(user: KeyboardConfigParser.parse(#"""
        [keybindings]
        "cmd+shift+b" = "missing.command"
        """#))
        let diagnostics = try failure(result)
        #expect(diagnostics.contains { $0.message.contains("missing.command") })
    }

    @Test("timeout must be a positive integer and defaults when omitted")
    func timeoutValidation() throws {
        #expect(try resolve().leaderTimeout == .milliseconds(1_800))
        for value in ["0", "-1", "\"1800\""] {
            let result = Keymap.resolve(user: KeyboardConfigParser.parse("""
            [keyboard]
            leader_timeout_ms = \(value)
            """))
            #expect(try failure(result).contains {
                $0.message.contains("positive integer")
            })
        }
        let floatResult = Keymap.resolve(user: KeyboardConfigParser.parse("""
        [keyboard]
        leader_timeout_ms = 1.5
        """))
        #expect(try failure(floatResult).contains { $0.message.contains("Floats") })
    }

    @Test("menu selection uses the latest remaining direct binding")
    func menuSelection() throws {
        let latest = try resolve(#"""
        [keybindings]
        "cmd+shift+b" = "view.toggle-sidebar"
        "ctrl+b" = "view.toggle-sidebar"
        """#)
        let latestStroke = try stroke("ctrl+b")
        #expect(latest.menuShortcuts[.toggleSidebar] == latestStroke)

        let fallback = try resolve(#"""
        [keybindings]
        "cmd+shift+b" = "view.toggle-sidebar"
        "ctrl+b" = "view.toggle-sidebar"
        "ctrl+b" = "unbind"
        """#)
        let fallbackStroke = try stroke("cmd+shift+b")
        #expect(fallback.menuShortcuts[.toggleSidebar] == fallbackStroke)
    }

    @Test("leader-only commands have no menu shortcut and commands may have many triggers")
    func menuEligibility() throws {
        let keymap = try resolve(#"""
        [keyboard]
        clear_default_keybindings = true
        [keybindings]
        "leader>p" = "project.new"
        "cmd+b" = "data.refresh"
        "ctrl+b" = "data.refresh"
        """#)
        #expect(keymap.menuShortcuts[.newProject] == nil)
        let latestStroke = try stroke("ctrl+b")
        #expect(keymap.menuShortcuts[.refresh] == latestStroke)
        #expect(command(at: try stroke("cmd+b"), in: keymap) == .refresh)
        #expect(command(at: try stroke("ctrl+b"), in: keymap) == .refresh)
    }

    @Test("one error prevents every otherwise-valid binding from publishing")
    func atomicFailure() throws {
        let result = Keymap.resolve(user: KeyboardConfigParser.parse(#"""
        [keybindings]
        "cmd+shift+b" = "data.refresh"
        "cmd+shift+x" = "unknown"
        """#))
        if case .success = result {
            Issue.record("Expected the complete candidate to fail")
        }
        #expect(try failure(result).count >= 1)
    }

    private func command(at stroke: KeyStroke, in keymap: ResolvedKeymap) -> CommandID? {
        command(in: keymap.root[stroke])
    }

    private func command(in node: ResolvedKeymap.Node?) -> CommandID? {
        guard case .command(let command) = node else { return nil }
        return command
    }

    private func prefix(
        at stroke: KeyStroke,
        in keymap: ResolvedKeymap
    ) -> [KeyStroke: ResolvedKeymap.Node]? {
        guard case .prefix(let node) = keymap.root[stroke] else { return nil }
        return node
    }

    private func failure(
        _ result: Result<ResolvedKeymap, ConfigDiagnostics>
    ) throws -> ConfigDiagnostics {
        guard case .failure(let diagnostics) = result else {
            Issue.record("Expected resolution failure")
            throw TestFailure.expectedFailure
        }
        return diagnostics
    }

    private enum TestFailure: Error { case expectedFailure }
}

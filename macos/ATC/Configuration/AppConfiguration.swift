struct AppConfiguration: Sendable {
    let keymap: ResolvedKeymap
    let terminal: TerminalPreferences

    init(keymap: ResolvedKeymap, terminal: TerminalPreferences = .init()) {
        self.keymap = keymap
        self.terminal = terminal
    }
}

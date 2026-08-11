import SwiftUI

struct AppCommands: Commands {
    let appModel: AppModel
    let configStore: ConfigurationStore

    /// The key window's state, via the platform's answer to "menu commands
    /// act on the focused window" — nil with no window key, which disables
    /// every windowed command below.
    @FocusedValue(WindowState.self) private var windowState

    private var context: CommandContext? {
        windowState.map { windowState in
            CommandContext(
                appModel: appModel,
                windowState: windowState,
                configStore: configStore
            )
        }
    }

    /// Context for commands that act on the app, not a window (refresh,
    /// configuration reload/reveal): they stay available with no key window
    /// — from Settings, or with every window closed. The throwaway
    /// WindowState only satisfies the context's shape; app-scoped commands
    /// never read it.
    private var appScopedContext: CommandContext {
        context
            ?? CommandContext(
                appModel: appModel,
                windowState: WindowState(),
                configStore: configStore
            )
    }

    var body: some Commands {
        CommandGroup(replacing: .newItem) {
            commandButton(.newThread)
            commandButton(.newTerminal)

            Divider()

            commandButton(.newProject)
        }

        CommandGroup(after: .sidebar) {
            commandButton(.toggleSidebar)
            commandButton(.showDashboard)
            commandButton(.refresh, appScoped: true)
            commandButton(.toggleCommandPalette)
            commandButton(.searchThreads)
            commandButton(.searchTerminals)
            Divider()
        }

        CommandGroup(after: .appSettings) {
            commandButton(.reloadConfiguration, appScoped: true)
            commandButton(.revealConfiguration, appScoped: true)
        }
    }

    @ViewBuilder
    private func commandButton(_ id: CommandID, appScoped: Bool = false) -> some View {
        let descriptor = CommandRegistry.descriptor(for: id)
        let context = appScoped ? appScopedContext : context
        let button = Button(descriptor.title) {
            if let context { CommandRegistry.execute(id, context: context) }
        }
        .disabled(context.map { !descriptor.availability($0).isAvailable } ?? true)

        if let shortcut = configStore.configuration.keymap.menuShortcuts[id]?.menuShortcut {
            button.keyboardShortcut(shortcut.key, modifiers: shortcut.modifiers)
        } else {
            button
        }
    }
}

private extension KeyStroke {
    var menuShortcut: (key: KeyEquivalent, modifiers: EventModifiers)? {
        guard key.count == 1, let character = key.first else { return nil }
        var eventModifiers: EventModifiers = []
        if modifiers.contains(.command) { eventModifiers.insert(.command) }
        if modifiers.contains(.control) { eventModifiers.insert(.control) }
        if modifiers.contains(.option) { eventModifiers.insert(.option) }
        if modifiers.contains(.shift) { eventModifiers.insert(.shift) }
        return (KeyEquivalent(character), eventModifiers)
    }
}

import SwiftUI

struct AppCommands: Commands {
    let appModel: AppModel
    let windowState: WindowState
    let configStore: KeyboardConfigStore

    private var context: CommandContext {
        CommandContext(
            appModel: appModel,
            windowState: windowState,
            configStore: configStore
        )
    }

    var body: some Commands {
        CommandGroup(replacing: .newItem) {
            commandButton(.newSession)
            commandButton(.newTerminal)

            Divider()

            commandButton(.newWorkspace)
            commandButton(.newProject)
        }

        CommandGroup(after: .sidebar) {
            commandButton(.toggleSidebar)
            commandButton(.showDashboard)
            commandButton(.refresh)
            commandButton(.toggleCommandPalette)
            Divider()
        }

        CommandGroup(after: .appSettings) {
            commandButton(.reloadConfiguration)
        }
    }

    @ViewBuilder
    private func commandButton(_ id: CommandID) -> some View {
        let descriptor = CommandRegistry.descriptor(for: id)
        let button = Button(descriptor.title) {
            CommandRegistry.execute(id, context: context)
        }
        .disabled(!descriptor.availability(context).isAvailable)

        if let shortcut = configStore.keymap.menuShortcuts[id]?.menuShortcut {
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

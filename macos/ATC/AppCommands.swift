import SwiftUI

/// Menu placement from the keyboard-shortcuts brief. Each item is one
/// closure-shaped command the future `session.new`/`workspace.new`/etc.
/// registry entries will invoke — no per-menu logic. The full shortcut MVP
/// (registry, config.toml, leader key) is a separate effort.
struct AppCommands: Commands {
    let appModel: AppModel
    let windowState: WindowState

    var body: some Commands {
        CommandGroup(replacing: .newItem) {
            // session.new / terminal.new: need an active open Workspace.
            Button("New Session") {
                windowState.startSessionKind = .agentSession
            }
            .keyboardShortcut("n", modifiers: .command)
            .disabled(!windowState.canStartSession(in: appModel))

            Button("New Terminal") {
                windowState.startSessionKind = .terminal
            }
            .keyboardShortcut("t", modifiers: .command)
            .disabled(!windowState.canStartSession(in: appModel))

            Divider()

            // workspace.new works everywhere; context-free without an
            // implied Project. ⌘⇧N is reassigned here from New Project.
            Button("New Workspace…") {
                windowState.presentCreateWorkspace(in: appModel)
            }
            .keyboardShortcut("n", modifiers: [.command, .shift])
            .disabled(appModel.runtimes.isEmpty)

            // project.new: menu-only (its old ⌘⇧N now creates Workspaces).
            Button("New Project…") {
                windowState.isCreateProjectPresented = true
            }
            .disabled(appModel.runtimes.isEmpty)
        }

        CommandGroup(after: .sidebar) {
            // view.toggle-sidebar: only meaningful in the Workspace shell.
            Button("Toggle Sidebar") {
                windowState.toggleSidebar()
            }
            .keyboardShortcut("b", modifiers: .command)
            .disabled(windowState.route != .workspace)

            // data.refresh: always available.
            Button("Refresh") {
                Task { await appModel.refreshAll() }
            }
            .keyboardShortcut("r", modifiers: .command)

            Divider()
        }
    }
}

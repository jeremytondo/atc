enum CommandID: String, CaseIterable, Sendable {
    case toggleSidebar = "view.toggle-sidebar"
    case toggleCommandPalette = "view.toggle-command-palette"
    case showDashboard = "view.show-dashboard"
    case newSession = "session.new"
    case newTerminal = "terminal.new"
    case newProject = "project.new"
    case newWorkspace = "workspace.new"
    case refresh = "data.refresh"
    case reloadConfiguration = "configuration.reload"
}

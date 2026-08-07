enum CommandID: String, CaseIterable, Sendable {
    case toggleSidebar = "view.toggle-sidebar"
    case toggleCommandPalette = "view.toggle-command-palette"
    case searchThreads = "view.search-threads"
    case searchTerminals = "view.search-terminals"
    case showDashboard = "view.show-dashboard"
    case newThread = "thread.new"
    case newTerminal = "terminal.new"
    case newProject = "project.new"
    case refresh = "data.refresh"
    case reloadConfiguration = "configuration.reload"
    case revealConfiguration = "configuration.reveal"
}

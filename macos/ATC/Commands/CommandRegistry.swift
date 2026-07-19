import AppKit
import Foundation

@MainActor
struct CommandContext {
    let appModel: AppModel
    let windowState: WindowState
    let configStore: ConfigurationStore
}

enum CommandAvailability: Equatable {
    case available
    case unavailable(reason: String)

    var isAvailable: Bool {
        self == .available
    }
}

enum CommandCategory: CaseIterable, Sendable {
    case general
    case projectsAndWorkspaces
    case sessionsAndTerminals
    case view

    var title: String {
        switch self {
        case .general: "General"
        case .projectsAndWorkspaces: "Projects & Workspaces"
        case .sessionsAndTerminals: "Sessions & Terminals"
        case .view: "View"
        }
    }
}

@MainActor
struct CommandDescriptor {
    let id: CommandID
    let title: String
    let category: CommandCategory
    var isPaletteEligible = true
    let availability: (CommandContext) -> CommandAvailability
    let perform: (CommandContext) -> Void
}

@MainActor
enum CommandRegistry {
    private static let sessionUnavailable =
        "Requires an open Workspace on a reachable Connection"
    private static let connectionUnavailable = "Requires a configured Connection"

    static var allDescriptors: [CommandDescriptor] {
        CommandID.allCases.map(descriptor(for:))
    }

    static func descriptor(for id: CommandID) -> CommandDescriptor {
        switch id {
        case .toggleSidebar:
            CommandDescriptor(
                id: id,
                title: "Toggle Sidebar",
                category: .view,
                availability: { _ in .available },
                perform: { $0.windowState.toggleSidebar() }
            )
        case .toggleCommandPalette:
            CommandDescriptor(
                id: id,
                title: "Toggle Command Palette",
                category: .view,
                isPaletteEligible: false,
                availability: {
                    $0.windowState.isSheetPresented
                        ? .unavailable(reason: "Not available while a dialog is open")
                        : .available
                },
                perform: { $0.windowState.isCommandPalettePresented.toggle() }
            )
        case .showDashboard:
            CommandDescriptor(
                id: id,
                title: "Show Dashboard",
                category: .view,
                availability: { _ in .available },
                perform: { $0.windowState.showDashboard() }
            )
        case .newSession:
            CommandDescriptor(
                id: id,
                title: "New Session",
                category: .sessionsAndTerminals,
                availability: sessionAvailability,
                perform: { $0.windowState.startSessionKind = .agentSession }
            )
        case .newTerminal:
            CommandDescriptor(
                id: id,
                title: "New Terminal",
                category: .sessionsAndTerminals,
                availability: sessionAvailability,
                perform: { $0.windowState.startSessionKind = .terminal }
            )
        case .newProject:
            CommandDescriptor(
                id: id,
                title: "New Project…",
                category: .projectsAndWorkspaces,
                availability: connectionAvailability,
                perform: { $0.windowState.isCreateProjectPresented = true }
            )
        case .newWorkspace:
            CommandDescriptor(
                id: id,
                title: "New Workspace…",
                category: .projectsAndWorkspaces,
                availability: connectionAvailability,
                perform: { $0.windowState.presentCreateWorkspace(in: $0.appModel) }
            )
        case .refresh:
            CommandDescriptor(
                id: id,
                title: "Refresh",
                category: .general,
                availability: { _ in .available },
                perform: { context in Task { await context.appModel.refreshAll() } }
            )
        case .reloadConfiguration:
            CommandDescriptor(
                id: id,
                title: "Reload Configuration",
                category: .general,
                availability: { _ in .available },
                perform: { $0.configStore.reload() }
            )
        case .revealConfiguration:
            CommandDescriptor(
                id: id,
                title: "Reveal Configuration",
                category: .general,
                availability: { _ in .available },
                perform: { context in
                    let fileURL = context.configStore.configURL
                    let directoryURL = context.configStore.configDirectoryURL
                    let selectedURL = FileManager.default.fileExists(atPath: fileURL.path)
                        ? fileURL
                        : directoryURL
                    NSWorkspace.shared.activateFileViewerSelecting([selectedURL])
                }
            )
        }
    }

    @discardableResult
    static func execute(
        _ id: CommandID,
        context: CommandContext
    ) -> CommandAvailability {
        let descriptor = descriptor(for: id)
        let availability = descriptor.availability(context)
        guard availability.isAvailable else { return availability }
        descriptor.perform(context)
        return .available
    }

    private static func sessionAvailability(_ context: CommandContext) -> CommandAvailability {
        context.windowState.canStartSession(in: context.appModel)
            ? .available
            : .unavailable(reason: sessionUnavailable)
    }

    private static func connectionAvailability(
        _ context: CommandContext
    ) -> CommandAvailability {
        context.appModel.runtimes.isEmpty
            ? .unavailable(reason: connectionUnavailable)
            : .available
    }
}

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

@MainActor
struct CommandDescriptor {
    let id: CommandID
    let title: String
    var isPaletteEligible = true
    let availability: (CommandContext) -> CommandAvailability
    let perform: (CommandContext) -> Void
}

@MainActor
enum CommandRegistry {
    private static let connectionUnavailable = "Requires a reachable Connection"
    private static let projectContextUnavailable = "Requires an open Project"
    private static let dialogUnavailable = "Not available while a dialog is open"
    private static let paletteOpenUnavailable = "Not available while the Command Palette is open"

    static var allDescriptors: [CommandDescriptor] {
        CommandID.allCases.map(descriptor(for:))
    }

    static func descriptor(for id: CommandID) -> CommandDescriptor {
        switch id {
        case .toggleSidebar:
            CommandDescriptor(
                id: id,
                title: "Toggle Sidebar",
                availability: { _ in .available },
                perform: { $0.windowState.toggleSidebar() }
            )
        case .toggleCommandPalette:
            CommandDescriptor(
                id: id,
                title: "Toggle Command Palette",
                isPaletteEligible: false,
                availability: {
                    $0.windowState.isSheetPresented
                        ? .unavailable(reason: dialogUnavailable)
                        : .available
                },
                perform: {
                    $0.windowState.commandPalettePresentation =
                        $0.windowState.commandPalettePresentation == nil ? .all : nil
                }
            )
        case .searchThreads:
            CommandDescriptor(
                id: id,
                title: "Search Threads…",
                isPaletteEligible: false,
                availability: scopedPaletteAvailability,
                perform: { $0.windowState.commandPalettePresentation = .threads }
            )
        case .searchTerminals:
            CommandDescriptor(
                id: id,
                title: "Search Terminals…",
                isPaletteEligible: false,
                availability: scopedPaletteAvailability,
                perform: { $0.windowState.commandPalettePresentation = .terminals }
            )
        case .showDashboard:
            CommandDescriptor(
                id: id,
                title: "Show Dashboard",
                availability: { _ in .available },
                perform: { $0.windowState.showDashboard() }
            )
        case .newThread:
            CommandDescriptor(
                id: id,
                title: "New Thread",
                availability: anyConnectionAvailability,
                perform: { $0.windowState.presentNewThread() }
            )
        case .newTerminal:
            CommandDescriptor(
                id: id,
                title: "New Terminal",
                availability: { context in
                    // A standalone terminal always belongs to a Project, and
                    // the only Project the window knows is its launch-local
                    // context.
                    guard let project = context.windowState.activeProject else {
                        return .unavailable(reason: projectContextUnavailable)
                    }
                    return context.appModel.canMutate(connectionID: project.connectionID)
                        ? .available
                        : .unavailable(reason: connectionUnavailable)
                },
                perform: { $0.windowState.newTerminalProject = $0.windowState.activeProject }
            )
        case .newProject:
            CommandDescriptor(
                id: id,
                title: "New Project…",
                availability: anyConnectionAvailability,
                perform: { $0.windowState.isCreateProjectPresented = true }
            )
        case .refresh:
            CommandDescriptor(
                id: id,
                title: "Refresh",
                availability: { _ in .available },
                perform: { context in Task { await context.appModel.refreshAll() } }
            )
        case .reloadConfiguration:
            CommandDescriptor(
                id: id,
                title: "Reload Configuration",
                availability: { _ in .available },
                perform: { $0.configStore.reload() }
            )
        case .revealConfiguration:
            CommandDescriptor(
                id: id,
                title: "Reveal Configuration",
                availability: { _ in .available },
                perform: { context in
                    let fileURL = context.configStore.configURL
                    let directoryURL = context.configStore.configDirectoryURL
                    let selectedURL =
                        FileManager.default.fileExists(atPath: fileURL.path)
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

    private static func anyConnectionAvailability(
        _ context: CommandContext
    ) -> CommandAvailability {
        context.appModel.runtimes.contains { context.appModel.canMutate(connectionID: $0.id) }
            ? .available
            : .unavailable(reason: connectionUnavailable)
    }

    private static func scopedPaletteAvailability(
        _ context: CommandContext
    ) -> CommandAvailability {
        if context.windowState.isSheetPresented {
            return .unavailable(reason: dialogUnavailable)
        }
        if context.windowState.commandPalettePresentation != nil {
            return .unavailable(reason: paletteOpenUnavailable)
        }
        return .available
    }
}

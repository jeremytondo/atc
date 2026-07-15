import Foundation

@MainActor
struct CommandContext {
    let appModel: AppModel
    let windowState: WindowState
    let configStore: KeyboardConfigStore
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
    let availability: (CommandContext) -> CommandAvailability
    let perform: (CommandContext) -> Void
}

@MainActor
enum CommandRegistry {
    private static let sessionUnavailable =
        "Requires an open Workspace on a reachable Connection"
    private static let connectionUnavailable = "Requires a configured Connection"

    static func descriptor(for id: CommandID) -> CommandDescriptor {
        switch id {
        case .toggleSidebar:
            CommandDescriptor(
                id: id,
                title: "Toggle Sidebar",
                availability: { _ in .available },
                perform: { $0.windowState.toggleSidebar() }
            )
        case .newSession:
            CommandDescriptor(
                id: id,
                title: "New Session",
                availability: sessionAvailability,
                perform: { $0.windowState.startSessionKind = .agentSession }
            )
        case .newTerminal:
            CommandDescriptor(
                id: id,
                title: "New Terminal",
                availability: sessionAvailability,
                perform: { $0.windowState.startSessionKind = .terminal }
            )
        case .newProject:
            CommandDescriptor(
                id: id,
                title: "New Project…",
                availability: connectionAvailability,
                perform: { $0.windowState.isCreateProjectPresented = true }
            )
        case .newWorkspace:
            CommandDescriptor(
                id: id,
                title: "New Workspace…",
                availability: connectionAvailability,
                perform: { $0.windowState.presentCreateWorkspace(in: $0.appModel) }
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

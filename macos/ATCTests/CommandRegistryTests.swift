import Foundation
import SwiftUI
import Testing
import ATCAPI
@testable import ATC

@MainActor
@Suite("Command registry")
struct CommandRegistryTests {
    private func makeModel() -> AppModel {
        let suite = "CommandRegistryTests.\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        defaults.removePersistentDomain(forName: suite)
        return AppModel(
            connections: ConnectionsStore(
                defaults: defaults,
                credentials: InMemoryCredentialStore()
            ),
            clientFactory: { _ in MockATCClient() },
            terminalRecoveryMonitor: .disabled()
        )
    }

    private func makeContext(
        model: AppModel,
        state: WindowState,
        store: ConfigurationStore? = nil
    ) -> CommandContext {
        CommandContext(
            appModel: model,
            windowState: state,
            configStore: store ?? ConfigurationStore(
                configURL: FileManager.default.temporaryDirectory
                    .appending(path: UUID().uuidString)
            )
        )
    }

    private func loadedContext() async throws -> (CommandContext, ConnectionRuntime) {
        let model = makeModel()
        let record = try model.addConnection(
            name: "A", urlString: "http://a:1", token: ""
        )
        let runtime = try #require(model.runtime(id: record.id))
        runtime.stopPolling()
        await runtime.refresh()
        let state = WindowState.ephemeral()
        #expect(state.activateWorkspace(
            WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_parser"),
            in: model
        ))
        return (makeContext(model: model, state: state), runtime)
    }

    @Test("descriptor enumeration is complete, unique, and stable")
    func descriptors() {
        let expectedIDs = [
            "view.toggle-sidebar", "view.toggle-command-palette",
            "view.show-dashboard", "session.new",
            "terminal.new", "project.new",
            "workspace.new", "data.refresh", "configuration.reload",
            "configuration.reveal",
        ]
        let descriptors = CommandRegistry.allDescriptors

        #expect(CommandID.allCases.map(\.rawValue) == expectedIDs)
        #expect(descriptors.map(\.id) == CommandID.allCases)
        #expect(Set(descriptors.map { $0.id.rawValue }).count == descriptors.count)
        #expect(descriptors.allSatisfy { !$0.title.isEmpty })
        #expect(descriptors.filter { !$0.isPaletteEligible }.map(\.id)
            == [.toggleCommandPalette])
    }

    @Test("categories use the shared presentation order and assignments")
    func categories() {
        #expect(CommandCategory.allCases.map(\.title) == [
            "General", "Projects & Workspaces", "Sessions & Terminals", "View",
        ])
        #expect(Dictionary(uniqueKeysWithValues: CommandRegistry.allDescriptors.map {
            ($0.id, $0.category)
        }) == [
            .toggleSidebar: .view,
            .toggleCommandPalette: .view,
            .showDashboard: .view,
            .newSession: .sessionsAndTerminals,
            .newTerminal: .sessionsAndTerminals,
            .newProject: .projectsAndWorkspaces,
            .newWorkspace: .projectsAndWorkspaces,
            .refresh: .general,
            .reloadConfiguration: .general,
            .revealConfiguration: .general,
        ])
    }

    @Test("the palette opener toggles and is unavailable for every sheet driver")
    func paletteOpener() {
        let model = makeModel()
        let state = WindowState.ephemeral()
        let context = makeContext(model: model, state: state)
        let descriptor = CommandRegistry.descriptor(for: .toggleCommandPalette)
        let unavailable = CommandAvailability.unavailable(
            reason: "Not available while a dialog is open"
        )

        #expect(descriptor.availability(context) == .available)
        CommandRegistry.execute(.toggleCommandPalette, context: context)
        #expect(state.isCommandPalettePresented)
        CommandRegistry.execute(.toggleCommandPalette, context: context)
        #expect(!state.isCommandPalettePresented)

        state.isCreateProjectPresented = true
        #expect(state.isSheetPresented)
        #expect(descriptor.availability(context) == unavailable)
        state.isCreateProjectPresented = false

        state.createWorkspaceContext = .init(mode: .free)
        #expect(state.isSheetPresented)
        #expect(descriptor.availability(context) == unavailable)
        state.createWorkspaceContext = nil

        state.startSessionKind = .terminal
        #expect(state.isSheetPresented)
        #expect(descriptor.availability(context) == unavailable)
        state.startSessionKind = nil
        #expect(!state.isSheetPresented)
        #expect(descriptor.availability(context) == .available)
    }

    @Test("availability follows the complete command truth table")
    func availability() async throws {
        let emptyModel = makeModel()
        let emptyState = WindowState.ephemeral()
        let empty = makeContext(model: emptyModel, state: emptyState)
        #expect(CommandRegistry.descriptor(for: .toggleSidebar).availability(empty) == .available)
        #expect(CommandRegistry.descriptor(for: .refresh).availability(empty) == .available)
        #expect(CommandRegistry.descriptor(for: .reloadConfiguration).availability(empty)
            == .available)
        #expect(CommandRegistry.descriptor(for: .revealConfiguration).availability(empty)
            == .available)
        let sessionAvailability = CommandRegistry.descriptor(for: .newSession)
            .availability(empty)
        #expect(sessionAvailability == .unavailable(
            reason: "Requires an open Workspace on a reachable Connection"
        ))
        for id in [CommandID.newWorkspace, .newProject] {
            #expect(CommandRegistry.descriptor(for: id).availability(empty)
                == .unavailable(reason: "Requires a configured Connection"))
        }

        let (loaded, _) = try await loadedContext()
        for id in [CommandID.newSession, .newTerminal, .newWorkspace, .newProject] {
            #expect(CommandRegistry.descriptor(for: id).availability(loaded) == .available)
        }
    }

    @Test("unavailable execution returns its reason and performs nothing")
    func unavailableExecution() {
        let model = makeModel()
        let state = WindowState.ephemeral()
        let context = makeContext(model: model, state: state)
        let outcome = CommandRegistry.execute(.newSession, context: context)
        #expect(outcome == .unavailable(
            reason: "Requires an open Workspace on a reachable Connection"
        ))
        #expect(state.startSessionKind == nil)
    }

    @Test("window and creation commands perform the original menu mutations")
    func windowMutations() async throws {
        let (context, _) = try await loadedContext()
        let state = context.windowState

        #expect(state.columnVisibility == .all)
        CommandRegistry.execute(.toggleSidebar, context: context)
        #expect(state.columnVisibility == .detailOnly)

        #expect(state.selectedContent != .dashboard)
        CommandRegistry.execute(.showDashboard, context: context)
        #expect(state.selectedContent == .dashboard)

        CommandRegistry.execute(.newSession, context: context)
        #expect(state.startSessionKind?.rawValue == StartSessionKind.agentSession.rawValue)
        state.startSessionKind = nil
        CommandRegistry.execute(.newTerminal, context: context)
        #expect(state.startSessionKind?.rawValue == StartSessionKind.terminal.rawValue)

        CommandRegistry.execute(.newWorkspace, context: context)
        #expect(state.createWorkspaceContext != nil)
        CommandRegistry.execute(.newProject, context: context)
        #expect(state.isCreateProjectPresented)
    }

    @Test("reload and refresh use the registry's always-available path")
    func appCommands() {
        let model = makeModel()
        let state = WindowState.ephemeral()
        let store = ConfigurationStore(
            configURL: FileManager.default.temporaryDirectory
                .appending(path: UUID().uuidString)
        )
        let context = makeContext(model: model, state: state, store: store)
        #expect(CommandRegistry.execute(.refresh, context: context) == .available)
        #expect(CommandRegistry.execute(.reloadConfiguration, context: context) == .available)
        #expect(store.configuration.keymap.generation == 1)
    }
}

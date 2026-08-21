import ATCAppServerAPI
import Foundation
import SwiftUI
import Testing

@testable import ATC

/// The command surface: every id has a descriptor, availability gates the
/// commands that need a reachable Connection or an active Project, and
/// executing an unavailable command mutates nothing.
///
/// Reason strings are presentation, so these tests assert on availability and
/// on the state each command actually changes, not on copy.
@MainActor
@Suite("Command registry")
struct CommandRegistryTests {
    private func makeContext(
        model: AppModel,
        state: WindowState,
        store: ConfigurationStore? = nil
    ) -> CommandContext {
        CommandContext(
            appModel: model,
            windowState: state,
            configStore: store
                ?? ConfigurationStore(
                    configURL: FileManager.default.temporaryDirectory
                        .appending(path: UUID().uuidString)
                )
        )
    }

    /// A context whose one Connection has completed a clean refresh, so
    /// `canMutate` is true and a Project context is established.
    private func connectedContext() async throws -> (CommandContext, TestModel) {
        let client = ScriptableAppServerClient()
        Fixtures.seed(client)
        let events = ScriptedEventStream()
        let test = try await makeModel(client: client, events: events)
        events.connect()
        await settle(until: { test.model.canMutate(connectionID: test.connectionID) })

        let state = WindowState()
        await state.openThread(test.threadRef("thr1"), in: test.model)
        return (makeContext(model: test.model, state: state), test)
    }

    @Test("every command id has exactly one titled descriptor")
    func descriptors() {
        #expect(
            CommandID.allCases.map(\.rawValue) == [
                "view.toggle-sidebar", "view.toggle-command-palette",
                "view.search-threads", "view.search-terminals",
                "view.show-dashboard", "thread.new", "thread.toggle-view-mode", "terminal.new",
                "project.new", "data.refresh", "configuration.reload",
                "configuration.reveal",
            ])
        let descriptors = CommandRegistry.allDescriptors
        #expect(descriptors.map(\.id) == CommandID.allCases)
        #expect(Set(descriptors.map(\.id)).count == descriptors.count)
        #expect(descriptors.allSatisfy { !$0.title.isEmpty })
        // The palette openers never list themselves.
        #expect(
            descriptors.filter { !$0.isPaletteEligible }.map(\.id)
                == [.toggleCommandPalette, .searchThreads, .searchTerminals])
    }

    @Test("commands that need no server are always available")
    func alwaysAvailableCommands() {
        let context = makeContext(model: makeEmptyAppModel(), state: WindowState())
        for id in [
            CommandID.toggleSidebar, .showDashboard, .refresh,
            .reloadConfiguration, .revealConfiguration,
        ] {
            #expect(CommandRegistry.descriptor(for: id).availability(context).isAvailable)
        }
    }

    @Test("New Thread and New Project require a Connection that can be mutated")
    func mutationCommandsRequireAReachableConnection() async throws {
        let cold = makeContext(model: makeEmptyAppModel(), state: WindowState())
        for id in [CommandID.newThread, .newProject] {
            #expect(!CommandRegistry.descriptor(for: id).availability(cold).isAvailable)
        }

        let (connected, _) = try await connectedContext()
        for id in [CommandID.newThread, .newProject] {
            #expect(CommandRegistry.descriptor(for: id).availability(connected).isAvailable)
        }
    }

    @Test("New Terminal additionally requires an active Project")
    func newTerminalRequiresAProject() async throws {
        let client = ScriptableAppServerClient()
        Fixtures.seed(client)
        let events = ScriptedEventStream()
        let test = try await makeModel(client: client, events: events)
        events.connect()
        await settle(until: { test.model.canMutate(connectionID: test.connectionID) })

        let state = WindowState()
        let context = makeContext(model: test.model, state: state)
        #expect(state.activeProject == nil)
        #expect(!CommandRegistry.descriptor(for: .newTerminal).availability(context).isAvailable)

        // Opening a thread establishes the launch-local Project context.
        await state.openThread(test.threadRef("thr1"), in: test.model)
        #expect(state.activeProject != nil)
        #expect(CommandRegistry.descriptor(for: .newTerminal).availability(context).isAvailable)
    }

    @Test("Toggle Chat/TUI needs a displayed thread and flips its remembered mode")
    func toggleThreadViewMode() async throws {
        let (context, test) = try await connectedContext()
        let ref = test.threadRef("thr1")
        let descriptor = CommandRegistry.descriptor(for: .toggleThreadViewMode)
        #expect(descriptor.availability(context).isAvailable)
        #expect(test.model.viewMode(for: ref) == .chat)

        CommandRegistry.execute(.toggleThreadViewMode, context: context)
        #expect(test.model.viewMode(for: ref) == .tui)
        await drainPendingTasks()

        context.windowState.showDashboard()
        #expect(!descriptor.availability(context).isAvailable)
    }

    @Test("the palette opener toggles and is unavailable while any sheet is up")
    func paletteOpener() {
        let state = WindowState()
        let context = makeContext(model: makeEmptyAppModel(), state: state)
        let descriptor = CommandRegistry.descriptor(for: .toggleCommandPalette)

        #expect(descriptor.availability(context).isAvailable)
        CommandRegistry.execute(.toggleCommandPalette, context: context)
        #expect(state.commandPalettePresentation == .all)
        CommandRegistry.execute(.toggleCommandPalette, context: context)
        #expect(state.commandPalettePresentation == nil)

        // A scoped palette closes on the toggle too.
        state.commandPalettePresentation = .threads
        CommandRegistry.execute(.toggleCommandPalette, context: context)
        #expect(state.commandPalettePresentation == nil)

        let presenters: [() -> Void] = [
            { state.isCreateProjectPresented = true },
            { state.newTerminalProject = ProjectRef(connectionID: UUID(), projectID: "prj") },
        ]
        for present in presenters {
            present()
            #expect(state.isSheetPresented)
            #expect(!descriptor.availability(context).isAvailable)
            state.isCreateProjectPresented = false
            state.newTerminalProject = nil
        }
        #expect(!state.isSheetPresented)
        #expect(descriptor.availability(context).isAvailable)
    }

    @Test("scoped search commands execute into their palette presentation")
    func scopedPaletteExecution() async throws {
        let (context, _) = try await connectedContext()
        let state = context.windowState

        for (id, presentation) in [
            (CommandID.searchThreads, CommandPalettePresentation.threads),
            (.searchTerminals, .terminals),
        ] {
            #expect(CommandRegistry.execute(id, context: context) == .available)
            #expect(state.commandPalettePresentation == presentation)
            state.commandPalettePresentation = nil
        }
    }

    @Test("scoped search is unavailable while a dialog or the palette is already open")
    func scopedPaletteAvailability() async throws {
        let (context, _) = try await connectedContext()
        let state = context.windowState
        let scoped = [CommandID.searchThreads, .searchTerminals]

        for id in scoped {
            #expect(CommandRegistry.descriptor(for: id).availability(context).isAvailable)
        }

        state.commandPalettePresentation = .all
        for id in scoped {
            #expect(!CommandRegistry.descriptor(for: id).availability(context).isAvailable)
        }
        state.commandPalettePresentation = nil

        state.isCreateProjectPresented = true
        for id in scoped {
            #expect(!CommandRegistry.descriptor(for: id).availability(context).isAvailable)
        }
    }

    @Test("an unavailable command returns its reason and performs nothing")
    func unavailableExecution() {
        let state = WindowState()
        let context = makeContext(model: makeEmptyAppModel(), state: state)
        let outcome = CommandRegistry.execute(.newThread, context: context)
        #expect(!outcome.isAvailable)
        #expect(state.newThreadContext == nil)
        #expect(!state.isSheetPresented)
    }

    @Test("window and creation commands perform their menu mutations")
    func commandMutations() async throws {
        let (context, _) = try await connectedContext()
        let state = context.windowState

        #expect(state.columnVisibility == .all)
        CommandRegistry.execute(.toggleSidebar, context: context)
        #expect(state.columnVisibility == .detailOnly)

        #expect(state.selectedContent != .dashboard)
        CommandRegistry.execute(.showDashboard, context: context)
        #expect(state.selectedContent == .dashboard)

        CommandRegistry.execute(.newThread, context: context)
        #expect(state.newThreadContext != nil)
        state.newThreadContext = nil

        CommandRegistry.execute(.newTerminal, context: context)
        #expect(state.newTerminalProject != nil)
        state.newTerminalProject = nil

        CommandRegistry.execute(.newProject, context: context)
        #expect(state.isCreateProjectPresented)
    }

    @Test("refresh and configuration reload always take the available path")
    func appCommands() {
        let store = ConfigurationStore(
            configURL: FileManager.default.temporaryDirectory
                .appending(path: UUID().uuidString)
        )
        let context = makeContext(
            model: makeEmptyAppModel(), state: WindowState(), store: store
        )
        #expect(CommandRegistry.execute(.refresh, context: context) == .available)
        #expect(CommandRegistry.execute(.reloadConfiguration, context: context) == .available)
        #expect(store.configuration.keymap.generation == 1)
    }
}

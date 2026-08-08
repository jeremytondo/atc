import Foundation
import Testing
import ATCAppServerAPI
@testable import ATC

/// What the palette offers, per presentation. Assertions stay structural —
/// which results appear, in what order, with what titles — so the palette's
/// row rendering can evolve without rewriting this suite.
@MainActor
@Suite("Command palette content")
struct CommandPaletteContentTests {
    private func keymap() throws -> ResolvedKeymap {
        try Keymap.resolve().get()
    }

    /// A connected model with a Project context, so command availability and
    /// thread/terminal results are all reachable.
    private func connectedContext() async throws -> (CommandContext, TestModel) {
        let client = ScriptableAppServerClient()
        Fixtures.seed(client)
        let events = ScriptedEventStream()
        let test = try await makeModel(client: client, events: events)
        events.connect()
        await settle(until: { test.model.canMutate(connectionID: test.connectionID) })
        await test.runtime.threads.loadArchivedIfNeeded()

        let state = WindowState()
        await state.openThread(test.threadRef("thr1"), in: test.model)
        let context = CommandContext(
            appModel: test.model,
            windowState: state,
            configStore: ConfigurationStore(
                configURL: FileManager.default.temporaryDirectory
                    .appending(path: UUID().uuidString)
            )
        )
        return (context, test)
    }

    private func results(
        _ query: String,
        context: CommandContext,
        presentation: CommandPalettePresentation = .all
    ) throws -> [PaletteResult] {
        CommandPaletteContent.results(
            query: query,
            keymap: try keymap(),
            context: context,
            presentation: presentation
        )
    }

    private func commandIDs(_ results: [PaletteResult]) -> [CommandID] {
        results.compactMap { result in
            guard case .command(let row) = result else { return nil }
            return row.id
        }
    }

    private func threadIDs(_ results: [PaletteResult]) -> [String] {
        results.compactMap { result in
            guard case .thread(let row) = result else { return nil }
            return row.ref.threadID
        }
    }

    private func terminalIDs(_ results: [PaletteResult]) -> [String] {
        results.compactMap { result in
            guard case .terminal(let row) = result else { return nil }
            return row.ref.terminalID
        }
    }

    @Test("an empty query lists every palette-eligible command and nothing else")
    func emptyQueryListsCommands() async throws {
        let (context, _) = try await connectedContext()
        let all = try results("", context: context)
        let eligible = CommandRegistry.allDescriptors.filter(\.isPaletteEligible).map(\.id)
        #expect(Set(commandIDs(all)) == Set(eligible))
        #expect(threadIDs(all).isEmpty)
        #expect(terminalIDs(all).isEmpty)
    }

    @Test("commands rank by fuzzy match, initials included")
    func commandsRankByMatch() async throws {
        let (context, _) = try await connectedContext()
        #expect(commandIDs(try results("dashboard", context: context)) == [.showDashboard])
        // Initials: "New Project".
        #expect(commandIDs(try results("np", context: context)).contains(.newProject))
        #expect(try results("zzzzz", context: context).isEmpty)
    }

    @Test("the Threads presentation lists active threads only")
    func threadsPresentation() async throws {
        let (context, _) = try await connectedContext()
        let all = try results("", context: context, presentation: .threads)
        #expect(Set(threadIDs(all)) == ["thr1", "thr2", "thr3"])
        // Archived threads are not navigable targets, and commands are out of
        // scope in a scoped presentation.
        #expect(!threadIDs(all).contains("thr_archived"))
        #expect(commandIDs(all).isEmpty)
        #expect(terminalIDs(all).isEmpty)
    }

    @Test("the Terminals presentation lists standalone terminals only")
    func terminalsPresentation() async throws {
        let (context, test) = try await connectedContext()
        // Opening thr1 created a thread-linked TUI terminal; it belongs to the
        // thread's row, never to the Terminals list.
        let linked = try #require(test.runtime.terminals.terminals.first { $0.threadId != nil })
        let all = try results("", context: context, presentation: .terminals)
        #expect(Set(terminalIDs(all)) == ["trm_live", "trm_ended"])
        #expect(!terminalIDs(all).contains(linked.id))
        #expect(commandIDs(all).isEmpty)
        #expect(threadIDs(all).isEmpty)
    }

    /// A scoped palette opens listing every target and narrows as the user
    /// types — that is what "Search Threads…" means. `CommandPaletteContent`
    /// currently hard-codes the type keyword for scoped presentations, which
    /// pins the "list everything" expansion on and makes the search field
    /// inert; passing `PaletteTypeKeyword.match(query)` there restores it.
    @Test("a scoped presentation filters its targets by the query")
    func scopedPresentationFilters() async throws {
        let client = ScriptableAppServerClient()
        Fixtures.seed(client)
        client.threads = client.threads.map { thread in
            guard thread.id == "thr2" else { return thread }
            var named = thread
            named.name = "Parser rewrite"
            return named
        }
        let events = ScriptedEventStream()
        let test = try await makeModel(client: client, events: events)
        events.connect()
        await settle(until: { test.model.canMutate(connectionID: test.connectionID) })
        let context = CommandContext(
            appModel: test.model,
            windowState: WindowState(),
            configStore: ConfigurationStore(
                configURL: FileManager.default.temporaryDirectory
                    .appending(path: UUID().uuidString)
            )
        )

        let matched = try results("parser", context: context, presentation: .threads)
        #expect(threadIDs(matched) == ["thr2"])
        #expect(try results("nothingmatchesthis", context: context, presentation: .threads).isEmpty)
    }
}

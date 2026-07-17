import Foundation
import Testing
import ATCAPI
@testable import ATC

@MainActor
@Suite("Command palette content")
struct CommandPaletteContentTests {
    @Test("type keywords require a complete three-character-or-longer prefix")
    func typeKeywordBoundaries() {
        #expect(PaletteTypeKeyword.match("se") == nil)
        for query in ["ses", "sess", "sessions"] {
            #expect(PaletteTypeKeyword.match(query) == .sessions)
        }
        #expect(PaletteTypeKeyword.match("sessionsx") == nil)
        #expect(PaletteTypeKeyword.match("session parser") == nil)
        #expect(PaletteTypeKeyword.match("SES") == .sessions)
        #expect(PaletteTypeKeyword.match("Ter") == .terminals)
        #expect(PaletteTypeKeyword.match(" \n work \t") == .workspaces)
    }

    @Test("palette-ineligible commands never appear")
    func eligibility() {
        let fixture = makeFixture()
        #expect(!commandRows(query: "", fixture: fixture).map(\.id)
            .contains(.toggleCommandPalette))
    }

    @Test("commands use stable alphabetical order for empty and filtered queries")
    func stableCommandOrdering() throws {
        let fixture = makeFixture()
        #expect(commandRows(query: "", fixture: fixture).map(\.title) == [
            "New Project…", "New Session", "New Terminal", "New Workspace…",
            "Refresh", "Reload Configuration", "Toggle Sidebar",
        ])
        #expect(commandRows(query: "new", fixture: fixture).map(\.title) == [
            "New Project…", "New Session", "New Terminal", "New Workspace…",
        ])
        #expect(CommandPaletteContent.isOrderedBefore(
            title: "Same", id: .newProject,
            thanTitle: "same", id: .newSession
        ))
        #expect(!CommandPaletteContent.isOrderedBefore(
            title: "same", id: .newSession,
            thanTitle: "Same", id: .newProject
        ))
    }

    @Test("nonmatching commands are excluded")
    func filtering() throws {
        let fixture = makeFixture()
        #expect(commandRows(query: "sidebar", fixture: fixture).map(\.id) == [.toggleSidebar])
        #expect(commandRows(query: "missing", fixture: fixture).isEmpty)
    }

    @Test("shortcuts follow direct rebinds and unbinds")
    func directShortcuts() throws {
        let rebound = makeFixture(config: #"""
        [keybindings]
        "ctrl+shift+r" = "data.refresh"
        """#)
        let reboundShortcut = try stroke("ctrl+shift+r")
        #expect(commandRow(.refresh, fixture: rebound).shortcut == reboundShortcut)

        let unbound = makeFixture(config: #"""
        [keybindings]
        "cmd+r" = "unbind"
        """#)
        #expect(commandRow(.refresh, fixture: unbound).shortcut == nil)
    }

    @Test("a command bound only through a Command Sequence has no shortcut")
    func sequenceIsNotAShortcut() throws {
        let fixture = makeFixture(config: #"""
        [keyboard]
        clear_default_keybindings = true
        [keybindings]
        "leader>x" = "project.new"
        """#)
        #expect(commandRow(.newProject, fixture: fixture).shortcut == nil)
    }

    @Test("availability is evaluated from the current command context")
    func availability() throws {
        let fixture = makeFixture()
        #expect(commandRow(.refresh, fixture: fixture).availability == .available)
        #expect(commandRow(.newSession, fixture: fixture).availability == .unavailable(
            reason: "Requires an open Workspace on a reachable Connection"
        ))
    }

    @Test("empty and whitespace queries return only eligible commands with live candidates")
    func emptyQueryIsCommandsOnly() async throws {
        let fixture = try await makeLiveFixture()
        let expected = commandRows(query: "", fixture: fixture).map(\.id)
        for query in ["", " \n\t "] {
            let results = results(query: query, fixture: fixture)
            #expect(results.count == expected.count)
            #expect(results.compactMap(\.commandRow?.id) == expected)
        }
    }

    @Test("nonempty results keep deterministic bucket and title order")
    func bucketOrdering() async throws {
        var client = MockATCClient()
        client.mockProjects = [paletteProject("project", name: "Project")]
        client.mockWorkspaces = [
            paletteWorkspace("wsp_z", project: "project", name: "Same"),
            paletteWorkspace("wsp_a", project: "project", name: "same"),
            paletteWorkspace("wsp_b", project: "project", name: "Alpha"),
        ]
        client.mockSessions = [
            paletteSession("ses_z", name: "Same", workspace: "wsp_b"),
            paletteSession("ses_a", name: "same", workspace: "wsp_b"),
            paletteSession("ses_b", name: "Alpha", workspace: "wsp_b"),
        ]
        let fixture = try await makeLiveFixture(client: client, workspaceID: "wsp_b")
        let projected = results(query: "a", fixture: fixture)

        let kinds = projected.map { result in
            switch result {
            case .command: "command"
            case .workspace: "workspace"
            case .session: "session"
            }
        }
        #expect(kinds == kinds.sorted { lhs, rhs in
            ["command": 0, "workspace": 1, "session": 2][lhs]!
                < ["command": 0, "workspace": 1, "session": 2][rhs]!
        })
        #expect(projected.compactMap(\.workspaceRow).map {
            "\($0.title):\($0.ref.workspaceID)"
        } == ["Alpha:wsp_b", "same:wsp_a", "Same:wsp_z"])
        #expect(projected.compactMap(\.sessionRow).map {
            "\($0.title):\($0.ref.sessionID)"
        } == ["Alpha:ses_b", "same:ses_a", "Same:ses_z"])
    }

    @Test("keyword expansion keeps bucket and alphabetical ordering")
    func keywordBucketOrdering() async throws {
        var client = MockATCClient()
        client.mockProjects = [paletteProject("project", name: "Project")]
        client.mockWorkspaces = [
            paletteWorkspace("wsp_z", project: "project", name: "Session Zoo"),
            paletteWorkspace("wsp_a", project: "project", name: "Session Alpha"),
        ]
        client.mockSessions = [
            paletteSession(
                "ses_z", name: "Zulu", action: "claude", workspace: "wsp_a"
            ),
            paletteSession(
                "ses_a", name: "Alpha", action: "claude", workspace: "wsp_a"
            ),
            paletteSession("terminal", name: "Shell Tools", workspace: "wsp_a"),
        ]
        let fixture = try await makeLiveFixture(client: client, workspaceID: "wsp_a")
        let projected = results(query: "ses", fixture: fixture)

        #expect(projected.compactMap(\.commandRow).map(\.title) == ["New Session"])
        #expect(projected.compactMap(\.workspaceRow).map(\.title) == [
            "Session Alpha", "Session Zoo",
        ])
        #expect(projected.compactMap(\.sessionRow).map(\.title) == ["Alpha", "Zulu"])
        #expect(projected.map { result in
            switch result {
            case .command: "command"
            case .workspace: "workspace"
            case .session: "session"
            }
        } == ["command", "workspace", "workspace", "session", "session"])
    }

    @Test("type expansion is additive and preserves title matches without duplicates")
    func keywordAdditivityAndDeduplication() async throws {
        var client = MockATCClient()
        client.mockProjects = [paletteProject("project", name: "Project")]
        client.mockWorkspaces = [
            paletteWorkspace("workspace", project: "project", name: "Workspace")
        ]
        client.mockSessions = [
            paletteSession(
                "title-match", name: "Terminal cleanup", workspace: "workspace"
            ),
            paletteSession(
                "category-only", name: "Shell tools", workspace: "workspace"
            ),
            paletteSession(
                "agent", name: "Agent work", action: "claude", workspace: "workspace"
            ),
        ]
        let fixture = try await makeLiveFixture(client: client, workspaceID: "workspace")
        let projected = results(query: "ter", fixture: fixture)
        let sessionRows = projected.compactMap(\.sessionRow)

        #expect(projected.compactMap(\.commandRow).map(\.title).contains("New Terminal"))
        #expect(sessionRows.map(\.ref.sessionID) == ["category-only", "title-match"])
        #expect(sessionRows.filter { $0.ref.sessionID == "title-match" }.count == 1)
        let titleMatch = try #require(sessionRows.first {
            $0.ref.sessionID == "title-match"
        })
        #expect(titleMatch.matchedRanges.map { String(titleMatch.title[$0]) } == ["Ter"])
        #expect(try #require(sessionRows.first {
            $0.ref.sessionID == "category-only"
        }).matchedRanges.isEmpty)
    }

    @Test("no Active Workspace omits Sessions and an unmatched query is empty")
    func noActiveWorkspace() async {
        let model = AppModel.preview()
        await model.refreshAll()
        let fixture = makeFixture(appModel: model)
        let projected = results(query: "Fix the parser", fixture: fixture)
        #expect(projected.isEmpty)
        #expect(projected.compactMap(\.sessionRow).isEmpty)
    }

    @Test("an unmatched query with live candidates has no per-bucket rows")
    func unmatchedQueryIsEntirelyEmpty() async throws {
        let fixture = try await makeLiveFixture()
        #expect(results(query: "zzz no such result", fixture: fixture).isEmpty)
    }

    @Test("an unreachable Connection keeps Sessions selectable but not Workspaces")
    func sessionsIgnoreReachability() async throws {
        let client = ScriptableClient()
        let model = AppModel.preview(client: client)
        await model.refreshAll()
        let state = WindowState.ephemeral()
        let connectionID = try #require(model.runtimes.first?.id)
        #expect(state.activateWorkspace(
            WorkspaceRef(connectionID: connectionID, workspaceID: "wsp_parser"),
            in: model
        ))
        let runtime = try #require(model.runtime(id: connectionID))
        runtime.stopPolling()
        client.shouldFail = true
        await runtime.refresh()
        #expect(runtime.reachability == .unreachable)

        let fixture = makeFixture(appModel: model, windowState: state)
        let projected = results(query: "parser", fixture: fixture)
        let workspaceRows = projected.compactMap(\.workspaceRow)
        #expect(!workspaceRows.isEmpty)
        #expect(workspaceRows.allSatisfy { !$0.availability.isAvailable })
        #expect(!projected.compactMap(\.sessionRow).isEmpty)
        #expect(projected.compactMap(\.sessionRow).allSatisfy {
            $0.title == "Fix the parser"
        })
    }

    private func makeFixture(
        config: String = "",
        appModel: AppModel? = nil,
        windowState: WindowState? = nil
    ) -> Fixture {
        let model = appModel ?? AppModel(
            connections: ConnectionsStore(
                defaults: ephemeralDefaults(),
                credentials: InMemoryCredentialStore()
            ),
            clientFactory: { _ in MockATCClient() },
            terminalRecoveryMonitor: .disabled()
        )
        let state = windowState ?? WindowState.ephemeral()
        let store = KeyboardConfigStore(
            configURL: FileManager.default.temporaryDirectory
                .appending(path: UUID().uuidString)
        )
        let keymap = try! Keymap.resolve(
            user: KeyboardConfigParser.parse(config)
        ).get()
        return Fixture(
            keymap: keymap,
            context: CommandContext(
                appModel: model,
                windowState: state,
                configStore: store
            )
        )
    }

    private func makeLiveFixture(
        client: MockATCClient = MockATCClient(),
        workspaceID: String = "wsp_parser"
    ) async throws -> Fixture {
        let model = AppModel.preview(client: client)
        await model.refreshAll()
        let state = WindowState.ephemeral()
        let connectionID = try #require(model.runtimes.first?.id)
        #expect(state.activateWorkspace(
            WorkspaceRef(connectionID: connectionID, workspaceID: workspaceID),
            in: model
        ))
        return makeFixture(appModel: model, windowState: state)
    }

    private func results(query: String, fixture: Fixture) -> [PaletteResult] {
        CommandPaletteContent.results(
            query: query,
            keymap: fixture.keymap,
            context: fixture.context
        )
    }

    private func commandRows(query: String, fixture: Fixture) -> [CommandPaletteRow] {
        results(query: query, fixture: fixture).compactMap(\.commandRow)
    }

    private func commandRow(_ id: CommandID, fixture: Fixture) -> CommandPaletteRow {
        try! #require(commandRows(query: "", fixture: fixture).first { $0.id == id })
    }

    private func stroke(_ text: String) throws -> KeyStroke {
        try KeyStroke.parse(text).get()
    }

    private struct Fixture {
        let keymap: ResolvedKeymap
        let context: CommandContext
    }
}

@MainActor
@Suite("Command palette Workspace candidates")
struct CommandPaletteWorkspaceResultTests {
    @Test("Workspace, Project, and Connection names match independently")
    func matchingFields() {
        let groups = groups(inputs: [input(
            connection: paletteConnection(1, name: "Remote Box"),
            projects: [paletteProject("prj", name: "Atlas Project")],
            workspaces: [paletteWorkspace(
                "wsp", project: "prj", name: "Alpha Workspace"
            )]
        )])
        for query in ["Alpha", "Atlas", "Remote"] {
            #expect(results(query: query, groups: groups).count == 1)
        }
        #expect(results(query: "Alpha Atlas", groups: groups).isEmpty)
    }

    @Test("equal Workspace titles use Connection and Workspace identity tie-breaks")
    func identityTieBreaks() {
        let projected = results(query: "Same", inputs: [
            input(
                connection: paletteConnection(2, name: "Second"),
                projects: [paletteProject("p2", name: "Project")],
                workspaces: [paletteWorkspace("wsp_b", project: "p2", name: "Same")]
            ),
            input(
                connection: paletteConnection(1, name: "First"),
                projects: [paletteProject("p1", name: "Project")],
                workspaces: [
                    paletteWorkspace("wsp_z", project: "p1", name: "same"),
                    paletteWorkspace("wsp_a", project: "p1", name: "Same"),
                ]
            ),
        ])
        #expect(projected.map {
            "\($0.ref.connectionID.uuidString):\($0.ref.workspaceID)"
        } == [
            "\(paletteConnectionID(1).uuidString):wsp_a",
            "\(paletteConnectionID(1).uuidString):wsp_z",
            "\(paletteConnectionID(2).uuidString):wsp_b",
        ])
    }

    @Test("only Workspace-name matches carry highlight ranges")
    func primaryLabelHighlighting() throws {
        let groups = groups(inputs: [input(
            connection: paletteConnection(1, name: "Remote Box"),
            projects: [paletteProject("prj", name: "Atlas Project")],
            workspaces: [paletteWorkspace("wsp", project: "prj", name: "Alpha Workspace")]
        )])
        let workspaceMatch = try #require(results(query: "Alpha", groups: groups).first)
        #expect(workspaceMatch.matchedRanges.map { String(workspaceMatch.title[$0]) } == ["Alpha"])
        #expect(try #require(results(query: "Atlas", groups: groups).first)
            .matchedRanges.isEmpty)
        #expect(try #require(results(query: "Remote", groups: groups).first)
            .matchedRanges.isEmpty)
    }

    @Test("shared groups exclude archived targets and retain every Connection")
    func archiveAndConnectionProjection() {
        let projected = results(query: "Workspace", inputs: [
            input(
                connection: paletteConnection(1, name: "First"),
                projects: [
                    paletteProject("active", name: "Active"),
                    paletteProject("archived", name: "Archived", archived: true),
                ],
                workspaces: [
                    paletteWorkspace(
                        "first", project: "active", name: "First Workspace"
                    ),
                    paletteWorkspace(
                        "hidden-workspace", project: "active",
                        name: "Hidden Workspace", archived: true
                    ),
                    paletteWorkspace(
                        "hidden-project", project: "archived", name: "Project Workspace"
                    ),
                ]
            ),
            input(
                connection: paletteConnection(2, name: "Second"),
                projects: [paletteProject("active", name: "Active")],
                workspaces: [paletteWorkspace(
                    "second", project: "active", name: "Second Workspace"
                )]
            ),
        ])
        #expect(projected.map(\.ref.workspaceID) == ["first", "second"])
        #expect(Set(projected.map(\.connectionName)) == ["First", "Second"])
    }

    @Test("Workspace keywords include every unarchived Workspace across Connections")
    func keywordExpansionAcrossConnections() {
        let projected = results(query: "  WoR  ", inputs: [
            input(
                connection: paletteConnection(1, name: "First"),
                projects: [paletteProject("p1", name: "Atlas")],
                workspaces: [
                    paletteWorkspace("alpha", project: "p1", name: "Alpha"),
                    paletteWorkspace(
                        "archived", project: "p1", name: "Archived", archived: true
                    ),
                ]
            ),
            input(
                connection: paletteConnection(2, name: "Second"),
                projects: [paletteProject("p2", name: "Beacon")],
                workspaces: [
                    paletteWorkspace("beta", project: "p2", name: "Beta")
                ],
                reachability: .unreachable
            ),
        ])

        #expect(projected.map(\.ref.workspaceID) == ["alpha", "beta"])
        #expect(projected.allSatisfy { $0.matchedRanges.isEmpty })
        #expect(projected[0].availability == .available)
        #expect(projected[1].availability == .unavailable(
            reason: "Requires a reachable Connection"
        ))
    }

    @Test("only connected Workspaces are available")
    func availability() {
        let projected = results(query: "Workspace", inputs: [
            input(
                connection: paletteConnection(1, name: "Connected"),
                projects: [paletteProject("p1", name: "Project")],
                workspaces: [paletteWorkspace("w1", project: "p1", name: "Workspace 1")],
                reachability: .connected
            ),
            input(
                connection: paletteConnection(2, name: "Unreachable"),
                projects: [paletteProject("p2", name: "Project")],
                workspaces: [paletteWorkspace("w2", project: "p2", name: "Workspace 2")],
                reachability: .unreachable
            ),
            input(
                connection: paletteConnection(3, name: "Unknown"),
                projects: [paletteProject("p3", name: "Project")],
                workspaces: [paletteWorkspace("w3", project: "p3", name: "Workspace 3")],
                reachability: .unknown
            ),
        ])
        #expect(projected[0].availability == .available)
        #expect(projected[1].availability == .unavailable(
            reason: "Requires a reachable Connection"
        ))
        #expect(projected[2].availability == .unavailable(
            reason: "Requires a reachable Connection"
        ))
    }

    @Test("the Active Workspace has no special result representation")
    func activeWorkspaceIsOrdinary() throws {
        let ref = WorkspaceRef(connectionID: paletteConnectionID(1), workspaceID: "active")
        let row = try #require(results(query: "Active", inputs: [input(
            connection: paletteConnection(1, name: "Local"),
            projects: [paletteProject("project", name: "Project")],
            workspaces: [paletteWorkspace(
                ref.workspaceID, project: "project", name: "Active Workspace"
            )]
        )]).first)
        #expect(row.ref == ref)
        #expect(row.title == "Active Workspace")
        #expect(row.availability == .available)
    }

    private func results(
        query: String,
        inputs: [ProjectsNavigatorGroups.Input]
    ) -> [WorkspaceResult] {
        results(query: query, groups: groups(inputs: inputs))
    }

    private func results(
        query: String,
        groups: ProjectsNavigatorGroups
    ) -> [WorkspaceResult] {
        CommandPaletteContent.workspaceResults(query: query, groups: groups)
    }

    private func groups(inputs: [ProjectsNavigatorGroups.Input]) -> ProjectsNavigatorGroups {
        ProjectsNavigatorGroups(inputs: inputs)
    }

}

@MainActor
@Suite("Command palette Session candidates")
struct CommandPaletteSessionResultTests {
    private let connectionID = paletteConnectionID(1)
    private let actions = [
        ATCAction(
            name: "claude", type: "agent", origin: "builtin",
            enabled: true, label: "Claude"
        ),
        ATCAction(
            name: "lazygit", origin: "custom", enabled: true,
            label: "LazyGit"
        ),
    ]

    @Test("only the Active Workspace's unarchived Sessions appear")
    func workspaceAndArchiveFiltering() {
        let active = WorkspaceRef(connectionID: connectionID, workspaceID: "active")
        let projected = results(query: "", active: active, sessions: [
            paletteSession("active", name: "Active", workspace: "active"),
            paletteSession("other", name: "Other", workspace: "other"),
            paletteSession(
                "archived", name: "Archived", workspace: "active", archived: true
            ),
        ])
        #expect(projected.map(\.ref.sessionID) == ["active"])
    }

    @Test("an archived Active Workspace still yields unarchived Sessions")
    func archivedActiveWorkspace() {
        let archivedWorkspace = WorkspaceRef(
            connectionID: connectionID,
            workspaceID: "archived-workspace"
        )
        #expect(results(
            query: "Session",
            active: archivedWorkspace,
            sessions: [paletteSession(
                "session", name: "Visible Session", workspace: "archived-workspace"
            )]
        ).map(\.ref.sessionID) == ["session"])
    }

    @Test("display names and kinds reuse SessionKind")
    func displayNamesAndKinds() {
        let active = WorkspaceRef(connectionID: connectionID, workspaceID: "active")
        let projected = results(query: "", active: active, sessions: [
            paletteSession("named", name: "Fix parser", action: "claude", workspace: "active"),
            paletteSession("shell", workspace: "active"),
            paletteSession("agent", action: "claude", workspace: "active"),
            paletteSession("tool", action: "lazygit", workspace: "active"),
        ])
        let byID = Dictionary(uniqueKeysWithValues: projected.map { ($0.ref.sessionID, $0) })
        #expect(byID["named"]?.title == "Fix parser")
        #expect(byID["named"]?.kind == .agent)
        #expect(byID["shell"]?.title == "Terminal")
        #expect(byID["shell"]?.kind == .terminal)
        #expect(byID["agent"]?.title == "Claude")
        #expect(byID["agent"]?.kind == .agent)
        #expect(byID["tool"]?.title == "LazyGit")
        #expect(byID["tool"]?.kind == .terminal)
    }

    @Test("Session and Terminal keywords expand only their classified kind")
    func keywordExpansionByKind() {
        let active = WorkspaceRef(connectionID: connectionID, workspaceID: "active")
        let candidates = [
            paletteSession(
                "agent-failed", name: "Agent Failed", action: "claude",
                workspace: "active", status: .failed
            ),
            paletteSession(
                "agent-terminated", name: "Finished Agent", action: "claude",
                workspace: "active", status: .terminated
            ),
            paletteSession(
                "shell-starting", name: "Shell Starting",
                workspace: "active", status: .starting
            ),
            paletteSession(
                "action-terminated", name: "Tool Done", action: "lazygit",
                workspace: "active", status: .terminated
            ),
            paletteSession(
                "unresolved-running", name: "Unknown Action", action: "missing",
                workspace: "active", status: .running
            ),
            paletteSession(
                "other-workspace", name: "Other Agent", action: "claude",
                workspace: "other"
            ),
            paletteSession(
                "archived", name: "Archived Agent", action: "claude",
                workspace: "active", archived: true
            ),
        ]

        let sessions = results(query: "ses", active: active, sessions: candidates)
        #expect(sessions.map(\.ref.sessionID) == ["agent-failed", "agent-terminated"])
        #expect(sessions.allSatisfy { $0.kind == .agent && $0.matchedRanges.isEmpty })

        let terminals = results(query: "ter", active: active, sessions: candidates)
        #expect(terminals.map(\.ref.sessionID) == [
            "shell-starting", "action-terminated", "unresolved-running",
        ])
        #expect(terminals.allSatisfy { $0.kind == .terminal && $0.matchedRanges.isEmpty })
    }

    @Test("cross-kind title matches stay additive under keyword expansion")
    func crossKindTitleMatches() throws {
        let active = WorkspaceRef(connectionID: connectionID, workspaceID: "active")
        let candidates = [
            paletteSession("shell", name: "Session log", workspace: "active"),
            paletteSession(
                "agent", name: "Terminate run", action: "claude", workspace: "active"
            ),
        ]

        let bySessionKeyword = results(query: "ses", active: active, sessions: candidates)
        #expect(bySessionKeyword.map(\.ref.sessionID) == ["shell", "agent"])
        let titleMatched = try #require(bySessionKeyword.first)
        #expect(titleMatched.kind == .terminal)
        #expect(titleMatched.matchedRanges.map {
            String(titleMatched.title[$0])
        } == ["Ses"])
        #expect(try #require(bySessionKeyword.last).matchedRanges.isEmpty)

        let byTerminalKeyword = results(query: "ter", active: active, sessions: candidates)
        #expect(byTerminalKeyword.map(\.ref.sessionID) == ["shell", "agent"])
        #expect(try #require(byTerminalKeyword.first).matchedRanges.isEmpty)
        let agentMatch = try #require(byTerminalKeyword.last)
        #expect(agentMatch.kind == .agent)
        #expect(agentMatch.matchedRanges.map { String(agentMatch.title[$0]) } == ["Ter"])
    }

    @Test("a two-character keyword prefix never expands")
    func shortPrefixDoesNotExpand() {
        let active = WorkspaceRef(connectionID: connectionID, workspaceID: "active")
        let projected = results(query: "se", active: active, sessions: [
            paletteSession("shell", name: "Session log", workspace: "active"),
            paletteSession(
                "agent", name: "Unrelated", action: "claude", workspace: "active"
            ),
        ])
        #expect(projected.map(\.ref.sessionID) == ["shell"])
    }

    @Test("hidden Action, status, and raw identifiers never match")
    func hiddenFieldsDoNotMatch() {
        let active = WorkspaceRef(connectionID: connectionID, workspaceID: "active")
        let session = paletteSession(
            "raw-identifier", name: "Public Name", action: "claude",
            workspace: "active", status: .failed
        )
        for query in ["claude", "failed", "raw-identifier"] {
            #expect(results(query: query, active: active, sessions: [session]).isEmpty)
        }
        #expect(results(query: "Public", active: active, sessions: [session]).count == 1)
    }

    @Test("the context entry point never reads Sessions from another Connection")
    func otherConnectionsAreExcluded() async throws {
        var activeClient = MockATCClient()
        activeClient.mockProjects = [paletteProject("p", name: "Project")]
        activeClient.mockWorkspaces = [paletteWorkspace("w", project: "p", name: "Workspace")]
        activeClient.mockSessions = [paletteSession(
            "active", name: "Only Active Connection", workspace: "w"
        )]

        var otherClient = MockATCClient()
        otherClient.mockProjects = [paletteProject("p", name: "Project")]
        otherClient.mockWorkspaces = [paletteWorkspace("w", project: "p", name: "Workspace")]
        otherClient.mockSessions = [paletteSession(
            "other", name: "Only Other Connection", workspace: "w"
        )]

        let model = AppModel.preview(connections: [
            (name: "Active", client: activeClient),
            (name: "Other", client: otherClient),
        ])
        await model.refreshAll()
        let state = WindowState.ephemeral()
        let activeConnection = try #require(model.runtimes.first?.id)
        #expect(state.activateWorkspace(
            WorkspaceRef(connectionID: activeConnection, workspaceID: "w"),
            in: model
        ))
        let fixture = commandContextFixture(appModel: model, windowState: state)
        let projected = CommandPaletteContent.results(
            query: "Only",
            keymap: fixture.keymap,
            context: fixture.context
        ).compactMap(\.sessionRow)
        #expect(projected.map(\.title) == ["Only Active Connection"])
    }

    private func results(
        query: String,
        active: WorkspaceRef,
        sessions: [Session]
    ) -> [SessionResult] {
        CommandPaletteContent.sessionResults(
            query: query,
            activeWorkspace: active,
            sessions: sessions,
            actions: actions
        )
    }
}

@MainActor
@Suite("Command palette projection scale")
struct CommandPaletteScaleTests {
    @Test("complete matching sets are never capped")
    func completeMatchingSet() {
        let workspaceInputs = (0..<50).map { connectionIndex in
            let projectID = "project-\(connectionIndex)"
            return input(
                connection: paletteConnection(
                    connectionIndex + 1,
                    name: "Connection \(connectionIndex)"
                ),
                projects: [paletteProject(projectID, name: "Scale Project")],
                workspaces: (0..<40).map { workspaceIndex in
                    paletteWorkspace(
                        "workspace-\(connectionIndex)-\(workspaceIndex)",
                        project: projectID,
                        name: "Scale Workspace \(connectionIndex)-\(workspaceIndex)"
                    )
                }
            )
        }
        let workspaceResults = CommandPaletteContent.workspaceResults(
            query: "Scale",
            groups: ProjectsNavigatorGroups(inputs: workspaceInputs)
        )
        #expect(workspaceResults.count == 2_000)

        let active = WorkspaceRef(
            connectionID: paletteConnectionID(1),
            workspaceID: "active"
        )
        let sessions = (0..<2_000).map {
            paletteSession(
                "session-\($0)", name: "Scale Session \($0)",
                workspace: active.workspaceID
            )
        }
        let sessionResults = CommandPaletteContent.sessionResults(
            query: "Scale",
            activeWorkspace: active,
            sessions: sessions,
            actions: []
        )
        #expect(sessionResults.count == 2_000)

        let keywordResults = CommandPaletteContent.sessionResults(
            query: "ter",
            activeWorkspace: active,
            sessions: sessions,
            actions: []
        )
        #expect(keywordResults.count == 2_000)
    }
}

private extension PaletteResult {
    var commandRow: CommandPaletteRow? {
        guard case .command(let row) = self else { return nil }
        return row
    }

    var workspaceRow: WorkspaceResult? {
        guard case .workspace(let row) = self else { return nil }
        return row
    }

    var sessionRow: SessionResult? {
        guard case .session(let row) = self else { return nil }
        return row
    }
}

@MainActor
private func commandContextFixture(
    appModel: AppModel,
    windowState: WindowState
) -> (keymap: ResolvedKeymap, context: CommandContext) {
    let store = KeyboardConfigStore(
        configURL: FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
    )
    return (
        try! Keymap.resolve(user: .empty).get(),
        CommandContext(
            appModel: appModel,
            windowState: windowState,
            configStore: store
        )
    )
}

private func ephemeralDefaults() -> UserDefaults {
    let suite = "CommandPaletteContentTests.\(UUID().uuidString)"
    let defaults = UserDefaults(suiteName: suite)!
    defaults.removePersistentDomain(forName: suite)
    return defaults
}

private func paletteConnectionID(_ number: Int) -> UUID {
    UUID(uuidString: String(format: "00000000-0000-0000-0000-%012d", number))!
}

private func paletteConnection(_ number: Int, name: String) -> ConnectionRecord {
    ConnectionRecord(
        id: paletteConnectionID(number),
        name: name,
        urlString: "http://connection-\(number):7331",
        token: ""
    )
}

private func paletteProject(
    _ id: String,
    name: String,
    archived: Bool = false
) -> Project {
    Project(
        id: id,
        name: name,
        workingDir: "/tmp/\(id)",
        createdAt: .now,
        updatedAt: .now,
        archivedAt: archived ? .now : nil
    )
}

private func paletteWorkspace(
    _ id: String,
    project: String,
    name: String,
    archived: Bool = false
) -> Workspace {
    Workspace(
        id: id,
        projectId: project,
        name: name,
        createdAt: .now,
        updatedAt: .now,
        archivedAt: archived ? .now : nil
    )
}

private func paletteSession(
    _ id: String,
    name: String? = nil,
    action: String? = nil,
    workspace: String,
    archived: Bool = false,
    status: SessionStatus = .running
) -> Session {
    Session(
        id: id,
        name: name,
        action: action,
        environment: "host",
        workingDir: "/tmp",
        status: status,
        attachable: false,
        createdAt: .now,
        updatedAt: .now,
        archivedAt: archived ? .now : nil,
        workspace: SessionWorkspace(id: workspace, name: workspace)
    )
}

private func input(
    connection: ConnectionRecord,
    projects: [Project],
    workspaces: [Workspace],
    reachability: Reachability = .connected
) -> ProjectsNavigatorGroups.Input {
    .init(
        connection: connection,
        reachability: reachability,
        projects: projects,
        workspaces: workspaces,
        sessions: []
    )
}

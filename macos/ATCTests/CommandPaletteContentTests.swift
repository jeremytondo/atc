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
            "Refresh", "Reload Configuration", "Reveal Configuration",
            "Show Dashboard", "Toggle Sidebar",
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
        } == [
            "Shell · Alpha:ses_b",
            "Shell · same:ses_a",
            "Shell · Same:ses_z",
        ])
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
                "ses_z", name: "Zulu", actionName: "Claude", workspace: "wsp_a"
            ),
            paletteSession(
                "ses_a", name: "Alpha", actionName: "Claude", workspace: "wsp_a"
            ),
            paletteSession("terminal", name: "Shell Tools", workspace: "wsp_a"),
        ]
        let fixture = try await makeLiveFixture(client: client, workspaceID: "wsp_a")
        let projected = results(query: "ses", fixture: fixture)

        #expect(projected.compactMap(\.commandRow).map(\.title) == ["New Session"])
        #expect(projected.compactMap(\.workspaceRow).map(\.title) == [
            "Session Alpha", "Session Zoo",
        ])
        #expect(projected.compactMap(\.sessionRow).map(\.title) == [
            "Claude · Alpha", "Claude · Zulu",
        ])
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
                "agent", name: "Agent work", actionName: "Claude", workspace: "workspace"
            ),
        ]
        let fixture = try await makeLiveFixture(client: client, workspaceID: "workspace")
        let projected = results(query: "ter", fixture: fixture)
        let sessionRows = projected.compactMap(\.sessionRow)

        #expect(projected.compactMap(\.commandRow).map(\.title).contains("New Terminal"))
        #expect(sessionRows.map(\.ref.sessionID) == ["title-match", "category-only"])
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
            $0.title == "Claude · Fix the parser"
        })
    }

    @Test("scoped blank queries list only the Active Workspace's classified Sessions")
    func scopedSessionKinds() async throws {
        var client = MockATCClient()
        client.mockProjects = [paletteProject("project", name: "Project")]
        client.mockWorkspaces = [
            paletteWorkspace("active", project: "project", name: "Active"),
            paletteWorkspace("other", project: "project", name: "Other"),
        ]
        client.mockSessions = [
            paletteSession(
                "agent-live", name: "Zulu Agent", actionName: "Claude", workspace: "active"
            ),
            paletteSession(
                "agent-ended", name: "Alpha Agent", actionName: "Claude",
                workspace: "active", status: .ended
            ),
            paletteSession("terminal-live", name: "Beta Terminal", workspace: "active"),
            paletteSession(
                "terminal-ended", name: "Delta Tool", actionName: "LazyGit",
                workspace: "active", status: .ended
            ),
            paletteSession(
                "outside", name: "Outside Agent", actionName: "Claude", workspace: "other"
            ),
        ]
        let fixture = try await makeLiveFixture(client: client, workspaceID: "active")

        let sessions = results(query: "", presentation: .sessions, fixture: fixture)
        #expect(sessions.count == 2)
        #expect(sessions.compactMap(\.sessionRow).map(\.title) == [
            "Claude · Alpha Agent", "Claude · Zulu Agent",
        ])
        #expect(sessions.compactMap(\.sessionRow).allSatisfy { $0.kind == .agent })

        let terminals = results(query: "", presentation: .terminals, fixture: fixture)
        #expect(terminals.count == 2)
        #expect(terminals.compactMap(\.sessionRow).map(\.title) == [
            "LazyGit · Delta Tool", "Shell · Beta Terminal",
        ])
        #expect(terminals.compactMap(\.sessionRow).allSatisfy { $0.kind == .terminal })
    }

    @Test("scoped typed queries never admit another result type")
    func scopedTypeIsolation() async throws {
        var client = MockATCClient()
        client.mockProjects = [paletteProject("project", name: "Project")]
        client.mockWorkspaces = [
            paletteWorkspace("active", project: "project", name: "Active Workspace")
        ]
        client.mockSessions = [
            paletteSession(
                "agent", name: "Agent Result", actionName: "Claude", workspace: "active"
            ),
            paletteSession("terminal", name: "Terminal Result", workspace: "active"),
        ]
        let fixture = try await makeLiveFixture(client: client, workspaceID: "active")

        #expect(results(
            query: "Refresh", presentation: .sessions, fixture: fixture
        ).isEmpty)
        #expect(results(
            query: "Terminal Result", presentation: .sessions, fixture: fixture
        ).isEmpty)
        #expect(results(
            query: "Agent Result", presentation: .terminals, fixture: fixture
        ).isEmpty)
        #expect(results(
            query: "Agent Result", presentation: .workspaces, fixture: fixture
        ).isEmpty)
    }

    @Test("type keywords expand only in the unscoped palette")
    func scopedKeywordsDoNotExpand() async throws {
        var client = MockATCClient()
        client.mockProjects = [paletteProject("project", name: "Project")]
        client.mockWorkspaces = [
            paletteWorkspace("alpha", project: "project", name: "Alpha"),
            paletteWorkspace("working", project: "project", name: "Workspace Notes"),
        ]
        client.mockSessions = [
            paletteSession(
                "session-match", name: "Session Notes", actionName: "Claude",
                workspace: "alpha"
            ),
            paletteSession(
                "session-other", name: "Agent Notes", actionName: "Claude",
                workspace: "alpha"
            ),
            paletteSession("terminal-match", name: "Terminal Notes", workspace: "alpha"),
            paletteSession("terminal-other", name: "Shell Notes", workspace: "alpha"),
        ]
        let fixture = try await makeLiveFixture(client: client, workspaceID: "alpha")

        #expect(results(
            query: "ses", presentation: .sessions, fixture: fixture
        ).compactMap(\.sessionRow).map(\.ref.sessionID) == ["session-match"])
        #expect(results(
            query: "ter", presentation: .terminals, fixture: fixture
        ).compactMap(\.sessionRow).map(\.ref.sessionID) == ["terminal-match"])
        // "worksp" is still a keyword prefix, but avoids the preview
        // Connection name "Workstation", which every scoped row may match.
        #expect(results(
            query: "worksp", presentation: .workspaces, fixture: fixture
        ).compactMap(\.workspaceRow).map(\.ref.workspaceID) == ["working"])

        let unscoped = results(query: "ses", presentation: .all, fixture: fixture)
            .compactMap(\.sessionRow)
        #expect(unscoped.map(\.ref.sessionID) == ["session-match", "session-other"])
        #expect(unscoped.allSatisfy { $0.kind == .agent })
    }

    @Test("Workspace scope includes Workspaces from unreachable Connections")
    func scopedWorkspacesAcrossConnections() async throws {
        let connectedClient = ScriptableClient()
        let disconnectedClient = ScriptableClient()
        let model = AppModel.preview(connections: [
            (name: "Connected", client: connectedClient),
            (name: "Disconnected", client: disconnectedClient),
        ])
        await model.refreshAll()
        let disconnectedRuntime = try #require(model.runtimes.first {
            $0.record.name == "Disconnected"
        })
        disconnectedRuntime.stopPolling()
        disconnectedClient.shouldFail = true
        await disconnectedRuntime.refresh()

        let fixture = makeFixture(appModel: model)
        let rows = results(query: "", presentation: .workspaces, fixture: fixture)
            .compactMap(\.workspaceRow)
        #expect(rows.count == 8)
        #expect(rows.map(\.title) == rows.map(\.title).sorted {
            $0.localizedCaseInsensitiveCompare($1) == .orderedAscending
        })
        #expect(rows.map(\.title).contains("Old experiment"))
        #expect(Set(rows.map(\.connectionName)) == ["Connected", "Disconnected"])
        #expect(rows.filter { $0.connectionName == "Disconnected" }.allSatisfy {
            $0.availability == .unavailable(reason: "Requires a reachable Connection")
        })
    }

    @Test("Session scopes are empty without an Active Workspace")
    func scopedSessionsWithoutActiveWorkspace() async {
        let model = AppModel.preview()
        await model.refreshAll()
        let fixture = makeFixture(appModel: model)
        #expect(results(query: "", presentation: .sessions, fixture: fixture).isEmpty)
        #expect(results(query: "", presentation: .terminals, fixture: fixture).isEmpty)
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
        let store = ConfigurationStore(
            configURL: FileManager.default.temporaryDirectory
                .appending(path: UUID().uuidString)
        )
        let keymap = try! Keymap.resolve(
            user: ConfigurationLoader.parse(config)
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

    private func results(
        query: String,
        presentation: CommandPalettePresentation = .all,
        fixture: Fixture
    ) -> [PaletteResult] {
        CommandPaletteContent.results(
            query: query,
            keymap: fixture.keymap,
            context: fixture.context,
            presentation: presentation
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

    @Test("shared groups retain every target and Connection")
    func connectionProjection() {
        let projected = results(query: "Workspace", inputs: [
            input(
                connection: paletteConnection(1, name: "First"),
                projects: [
                    paletteProject("active", name: "Active"),
                    paletteProject("experiment", name: "Experiment"),
                ],
                workspaces: [
                    paletteWorkspace(
                        "first", project: "active", name: "First Workspace"
                    ),
                    paletteWorkspace(
                        "extra-workspace", project: "active",
                        name: "Extra Workspace"
                    ),
                    paletteWorkspace(
                        "project-workspace", project: "experiment", name: "Project Workspace"
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
        #expect(projected.map(\.ref.workspaceID) == ["extra-workspace", "first", "project-workspace", "second"])
        #expect(Set(projected.map(\.connectionName)) == ["First", "Second"])
    }

    @Test("Workspace keywords include every Workspace across Connections")
    func keywordExpansionAcrossConnections() {
        let projected = results(query: "  WoR  ", inputs: [
            input(
                connection: paletteConnection(1, name: "First"),
                projects: [paletteProject("p1", name: "Atlas")],
                workspaces: [
                    paletteWorkspace("alpha", project: "p1", name: "Alpha"),
                    paletteWorkspace("extra", project: "p1", name: "Extra"),
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

        #expect(projected.map(\.ref.workspaceID) == ["alpha", "beta", "extra"])
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
        CommandPaletteContent.workspaceResults(
            query: query,
            groups: groups,
            keyword: PaletteTypeKeyword.match(query)
        )
    }

    private func groups(inputs: [ProjectsNavigatorGroups.Input]) -> ProjectsNavigatorGroups {
        ProjectsNavigatorGroups(inputs: inputs)
    }

}

@MainActor
@Suite("Command palette Session candidates")
struct CommandPaletteSessionResultTests {
    private let connectionID = paletteConnectionID(1)

    @Test("the Active Workspace's Live and Ended Sessions appear")
    func workspaceFiltering() {
        let active = WorkspaceRef(connectionID: connectionID, workspaceID: "active")
        let projected = results(query: "", active: active, sessions: [
            paletteSession("active", name: "Active", workspace: "active"),
            paletteSession("other", name: "Other", workspace: "other"),
            paletteSession("ended", name: "Ended", workspace: "active", status: .ended),
        ])
        #expect(projected.map(\.ref.sessionID) == ["active", "ended"])
    }

    @Test("display names and kinds reuse SessionKind")
    func displayNamesAndKinds() {
        let active = WorkspaceRef(connectionID: connectionID, workspaceID: "active")
        let projected = results(query: "", active: active, sessions: [
            paletteSession("named", name: "Fix parser", actionName: "Claude", workspace: "active"),
            paletteSession("shell", workspace: "active"),
            paletteSession("agent", actionName: "Claude", workspace: "active"),
            paletteSession("tool", actionName: "LazyGit", workspace: "active"),
        ])
        let byID = Dictionary(uniqueKeysWithValues: projected.map { ($0.ref.sessionID, $0) })
        #expect(byID["named"]?.title == "Claude · Fix parser")
        #expect(byID["named"]?.kind == .agent)
        #expect(byID["shell"]?.title == "Shell")
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
                "agent-failed", name: "Agent Failed", actionName: "Claude",
                workspace: "active", status: .ended
            ),
            paletteSession(
                "agent-terminated", name: "Finished Agent", actionName: "Claude",
                workspace: "active", status: .ended
            ),
            paletteSession(
                "shell-starting", name: "Shell Starting",
                workspace: "active", status: .live
            ),
            paletteSession(
                "action-terminated", name: "Tool Done", actionName: "LazyGit",
                workspace: "active", status: .ended
            ),
            paletteSession(
                "unresolved-running", name: "Unknown Action", actionName: "Missing",
                workspace: "active", status: .live
            ),
            paletteSession(
                "other-workspace", name: "Other Agent", actionName: "Claude",
                workspace: "other"
            ),
        ]

        let sessions = results(query: "ses", active: active, sessions: candidates)
        #expect(sessions.map(\.ref.sessionID) == ["agent-failed", "agent-terminated"])
        #expect(sessions.allSatisfy { $0.kind == .agent && $0.matchedRanges.isEmpty })

        let terminals = results(query: "ter", active: active, sessions: candidates)
        #expect(terminals.map(\.ref.sessionID) == [
            "action-terminated", "unresolved-running", "shell-starting",
        ])
        #expect(terminals.allSatisfy { $0.kind == .terminal && $0.matchedRanges.isEmpty })
    }

    @Test("cross-kind title matches stay additive under keyword expansion")
    func crossKindTitleMatches() throws {
        let active = WorkspaceRef(connectionID: connectionID, workspaceID: "active")
        let candidates = [
            paletteSession("shell", name: "Session log", workspace: "active"),
            paletteSession(
                "agent", name: "Terminate run", actionName: "Claude", workspace: "active"
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
        #expect(byTerminalKeyword.map(\.ref.sessionID) == ["agent", "shell"])
        let agentMatch = try #require(byTerminalKeyword.first)
        #expect(agentMatch.kind == .agent)
        #expect(agentMatch.matchedRanges.map { String(agentMatch.title[$0]) } == ["Ter"])
        #expect(try #require(byTerminalKeyword.last).matchedRanges.isEmpty)
    }

    @Test("a two-character keyword prefix never expands")
    func shortPrefixDoesNotExpand() {
        let active = WorkspaceRef(connectionID: connectionID, workspaceID: "active")
        let projected = results(query: "se", active: active, sessions: [
            paletteSession("shell", name: "Session log", workspace: "active"),
            paletteSession(
                "agent", name: "Unrelated", actionName: "Claude", workspace: "active"
            ),
        ])
        #expect(projected.map(\.ref.sessionID) == ["shell"])
    }

    @Test("numeric, identity, custom-name, and combined Session queries are additive")
    func indexedQueries() {
        let active = WorkspaceRef(connectionID: connectionID, workspaceID: "active")
        let candidates = [
            paletteSession(
                "claude-two", index: 2, name: "API migration",
                actionName: "Claude", workspace: "active"
            ),
            paletteSession(
                "codex-three", index: 3, name: "Parser cleanup",
                actionName: "Codex", workspace: "active"
            ),
            paletteSession(
                "title-digit", index: 9, name: "Version 2 shell",
                workspace: "active"
            ),
        ]

        #expect(results(
            query: "2", active: active, sessions: candidates
        ).map(\.ref.sessionID) == ["claude-two", "title-digit"])
        #expect(results(
            query: "claude", active: active, sessions: candidates
        ).map(\.ref.sessionID) == ["claude-two"])
        #expect(results(
            query: "migration", active: active, sessions: candidates
        ).map(\.ref.sessionID) == ["claude-two"])
        #expect(results(
            query: "2 claude", active: active, sessions: candidates
        ).map(\.ref.sessionID) == ["claude-two"])
        #expect(results(
            query: "2 codex", active: active, sessions: candidates
        ).isEmpty)
    }

    @Test("index is the default Session tie-break ahead of title and legacy IDs")
    func indexTieBreak() {
        let active = WorkspaceRef(connectionID: connectionID, workspaceID: "active")
        let projected = results(query: "claude", active: active, sessions: [
            paletteSession(
                "legacy", name: "Alpha", actionName: "Claude", workspace: "active"
            ),
            paletteSession(
                "seven", index: 7, name: "Beta", actionName: "Claude", workspace: "active"
            ),
            paletteSession(
                "two", index: 2, name: "Zulu", actionName: "Claude", workspace: "active"
            ),
        ])

        #expect(projected.map(\.ref.sessionID) == ["two", "seven", "legacy"])
        #expect(projected.map(\.identity.index) == [2, 7, nil])
    }

    @Test("identity and custom names match while status and raw identifiers stay hidden")
    func hiddenFieldsDoNotMatch() {
        let active = WorkspaceRef(connectionID: connectionID, workspaceID: "active")
        let session = paletteSession(
            "raw-identifier", name: "Public Name", actionName: "Claude",
            workspace: "active", status: .ended
        )
        for query in ["failed", "raw-identifier"] {
            #expect(results(query: query, active: active, sessions: [session]).isEmpty)
        }
        #expect(results(query: "claude", active: active, sessions: [session]).count == 1)
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
            context: fixture.context,
            presentation: .all
        ).compactMap(\.sessionRow)
        #expect(projected.map(\.title) == ["Shell · Only Active Connection"])
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
            keyword: PaletteTypeKeyword.match(query)
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
            groups: ProjectsNavigatorGroups(inputs: workspaceInputs),
            keyword: nil
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
            keyword: nil
        )
        #expect(sessionResults.count == 2_000)

        let keywordResults = CommandPaletteContent.sessionResults(
            query: "ter",
            activeWorkspace: active,
            sessions: sessions,
            keyword: .terminals
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
    let store = ConfigurationStore(
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
    name: String
) -> Project {
    Project(
        id: id,
        name: name,
        workingDir: "/tmp/\(id)",
        createdAt: .now,
        updatedAt: .now
    )
}

private func paletteWorkspace(
    _ id: String,
    project: String,
    name: String
) -> Workspace {
    Workspace(
        id: id,
        projectId: project,
        name: name,
        createdAt: .now,
        updatedAt: .now
    )
}

private func paletteSession(
    _ id: String,
    index: Int? = nil,
    name: String? = nil,
    actionName: String? = nil,
    workspace: String,
    status: SessionStatus = .live
) -> Session {
    return Session(
        id: id,
        sessionIndex: index,
        name: name,
        actionId: actionName.map { "act_\($0.lowercased())" },
        actionName: actionName,
        isAgent: actionName == "Claude",
        workingDir: "/tmp",
        status: status,
        createdAt: .now,
        updatedAt: .now,
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

import Foundation
import Testing
import ATCAPI
@testable import ATC

@Suite("SessionKind")
struct SessionKindTests {
    private func session(
        index: Int? = nil,
        name: String? = nil,
        actionId: String? = "act_x",
        actionName: String? = "Action",
        isAgent: Bool = false
    ) -> Session {
        Session(
            id: "ses_x",
            sessionIndex: index,
            name: name,
            actionId: actionId,
            actionName: actionName,
            isAgent: isAgent,
            workingDir: "/home/dev",
            status: .live,
            createdAt: .now,
            updatedAt: .now
        )
    }

    @Test("Interactive Shell is always a Terminal")
    func shellIsTerminal() {
        #expect(SessionKind.classify(
            session: session(actionId: nil, actionName: nil, isAgent: true)
        ) == .terminal)
    }

    @Test("copied isAgent classifies Action sessions")
    func copiedClassification() {
        #expect(SessionKind.classify(session: session(isAgent: true)) == .agent)
        #expect(SessionKind.classify(session: session(isAgent: false)) == .terminal)
    }

    @Test("classification does not need the current Action registry")
    func deletedActionKeepsClassification() {
        let deletedAgent = session(
            actionId: "act_deleted",
            actionName: "Deleted Agent",
            isAgent: true
        )

        #expect(SessionKind.classify(session: deletedAgent) == .agent)
        #expect(SessionIdentity(session: deletedAgent).identityText == "Deleted Agent")
    }

    @Test("Agent identity stays primary and the custom name stays secondary")
    func identityProjection() {
        let agent = SessionIdentity(session: session(
            index: 2,
            name: "API migration",
            actionName: "Codex",
            isAgent: true
        ))
        #expect(agent.index == 2)
        #expect(agent.kind == .agent)
        #expect(agent.identityText == "Codex")
        #expect(agent.customName == "API migration")
        #expect(agent.primaryText == "Codex")
        #expect(agent.secondaryText == "API migration")
        #expect(agent.fullLabel == "Codex · API migration")
        #expect(agent.indexedLabel == "[2] Codex · API migration")
        #expect(agent.indexedIdentityLabel == "[2] Codex")
        #expect(agent.accessibilityLabel == "Session 2, Codex, API migration")

        let action = SessionIdentity(session: session(actionName: "Editor"))
        #expect(action.kind == .terminal)
        #expect(action.identityText == "Editor")
        #expect(action.primaryText == "Editor")
        #expect(action.secondaryText == nil)
        #expect(action.fullLabel == "Editor")
    }

    @Test("Terminal custom names replace the launch identity in visible labels")
    func terminalCustomNameProjection() {
        let action = SessionIdentity(session: session(
            index: 4,
            name: "Editor",
            actionName: "Neovim"
        ))
        #expect(action.identityText == "Neovim")
        #expect(action.customName == "Editor")
        #expect(action.primaryText == "Editor")
        #expect(action.secondaryText == nil)
        #expect(action.fullLabel == "Editor")
        #expect(action.indexedLabel == "[4] Editor")
        #expect(action.indexedIdentityLabel == "[4] Neovim")
        #expect(action.accessibilityLabel == "Session 4, Editor")

        let shell = SessionIdentity(session: session(
            name: "Scratch",
            actionId: nil,
            actionName: nil
        ))
        #expect(shell.identityText == "Shell")
        #expect(shell.fullLabel == "Scratch")
        #expect(shell.accessibilityLabel == "Scratch")

        let duplicateShell = SessionIdentity(session: session(
            name: "Shell",
            actionId: nil,
            actionName: nil
        ))
        #expect(duplicateShell.fullLabel == "Shell")
    }

    @Test("legacy identity omits the index and blank custom names")
    func legacyIdentity() {
        let identity = SessionIdentity(session: session(
            name: " \n ",
            actionName: "Claude",
            isAgent: true
        ))
        #expect(identity.index == nil)
        #expect(identity.customName == nil)
        #expect(identity.indexedLabel == "Claude")
        #expect(identity.accessibilityLabel == "Claude")
    }
}

@Suite("Workspace Session groups")
struct WorkspaceSessionGroupsTests {
    @Test("groups ascend by shared index and place legacy Sessions last")
    func indexedOrdering() {
        let workspace = WorkspaceRef(connectionID: UUID(), workspaceID: "wsp")
        let base = Date(timeIntervalSince1970: 1_000)
        let groups = WorkspaceSessionGroups(workspace: workspace, sessions: [
            groupedSession("agent-legacy-new", createdAt: base.addingTimeInterval(20), isAgent: true),
            groupedSession("terminal-six", index: 6, createdAt: base, isAgent: false),
            groupedSession("agent-four", index: 4, createdAt: base, isAgent: true),
            groupedSession("terminal-two", index: 2, createdAt: base, isAgent: false),
            groupedSession("agent-one", index: 1, createdAt: base, isAgent: true),
            groupedSession("terminal-legacy", createdAt: base.addingTimeInterval(10), isAgent: false),
            groupedSession("agent-legacy-old", createdAt: base.addingTimeInterval(10), isAgent: true),
        ])

        #expect(groups.sessions.map(\.identity.index) == [1, 4, nil, nil])
        #expect(groups.sessions.map(\.ref.sessionID) == [
            "agent-one", "agent-four", "agent-legacy-old", "agent-legacy-new",
        ])
        #expect(groups.terminals.map(\.identity.index) == [2, 6, nil])
        #expect(groups.terminals.map(\.ref.sessionID) == [
            "terminal-two", "terminal-six", "terminal-legacy",
        ])
    }

    private func groupedSession(
        _ id: String,
        index: Int? = nil,
        createdAt: Date,
        isAgent: Bool
    ) -> Session {
        Session(
            id: id,
            sessionIndex: index,
            actionId: "act_\(id)",
            actionName: isAgent ? "Codex" : "Editor",
            isAgent: isAgent,
            workingDir: "/tmp",
            status: .live,
            createdAt: createdAt,
            updatedAt: createdAt,
            workspace: SessionWorkspace(id: "wsp", name: "Workspace")
        )
    }
}

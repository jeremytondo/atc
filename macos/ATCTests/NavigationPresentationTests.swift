import Foundation
import Testing
import ATCAPI
@testable import ATC

@MainActor
@Suite("Navigation presentation")
struct NavigationPresentationTests {
    @Test("Navigator selector exposes all states and Active Workspace gating")
    func navigatorSelectorStates() {
        #expect(NavigatorID.allCases.map(\.selectorLabel) == ["Projects", "Workspace", "Files"])

        let unavailable = NavigatorSelectorOption.all(hasActiveWorkspace: false)
        #expect(unavailable.map(\.id) == [.projects, .workspace, .file])
        #expect(unavailable.map(\.isEnabled) == [true, false, false])
        #expect(unavailable[1].help == "Requires an Active Workspace")
        #expect(unavailable[2].help == "Requires an Active Workspace")

        let available = NavigatorSelectorOption.all(hasActiveWorkspace: true)
        #expect(available.map(\.isEnabled) == [true, true, true])
        #expect(available.map(\.help) == NavigatorID.allCases.map(\.label))
        #expect(FileNavigatorView.unavailableMessage == "File navigation is not available yet")
    }

    @Test("Workspace Switcher projects no-active and active context")
    func workspaceSwitcherStates() {
        let project = Project(
            id: "prj", name: "Shared", workingDir: "/tmp",
            createdAt: .now, updatedAt: .now
        )
        let workspace = Workspace(
            id: "wsp", projectId: project.id, name: "Parser",
            createdAt: .now, updatedAt: .now
        )
        let noActive = WorkspaceSwitcherPresentation.noActiveWorkspace
        #expect(noActive.projectName == nil)
        #expect(noActive.label == "Select Workspace…")
        #expect(noActive.help == "Select an Active Workspace")
        #expect(noActive.session == nil)

        let active = WorkspaceSwitcherPresentation(
            project: project,
            workspace: workspace
        )
        #expect(active.projectName == "Shared")
        #expect(active.workspaceName == "Parser")
        #expect(active.label == "Shared › Parser")
        #expect(active.help == active.label)
        #expect(active.session == nil)

        let session = navigationSession(
            "ses", index: 7, name: "Migration", actionName: "Codex",
            isAgent: true
        )
        let selected = WorkspaceSwitcherPresentation(
            project: project,
            workspace: workspace,
            session: session
        )
        #expect(selected.session?.indexedLabel == "[7] Codex · Migration")
        #expect(selected.help == "Shared › Parser › [7] Codex · Migration")
    }

    @Test("Session rename request edits only the custom name and supports clearing")
    func sessionRenameRequest() {
        let ref = SessionRef(connectionID: UUID(), sessionID: "ses_123")
        let identity = SessionIdentity(session: navigationSession(
            ref.sessionID,
            index: 2,
            name: "Current name",
            actionName: "Claude",
            isAgent: true
        ))
        var request = SessionRenameRequest(ref: ref, identity: identity, kind: .agent)

        #expect(request.ref == ref)
        #expect(request.dialogTitle == "Rename Session “[2] Claude · Current name”")
        #expect(request.draft == "Current name")
        #expect(!request.canSubmit)

        request.draft = " \n "
        #expect(request.canSubmit)
        #expect(request.normalizedName == nil)

        request.draft = "  New name\n"
        #expect(request.normalizedName == "New name")
        #expect(request.canSubmit)

        let unnamed = SessionIdentity(session: navigationSession(
            ref.sessionID,
            index: 3,
            actionName: nil,
            isAgent: false
        ))
        request = SessionRenameRequest(ref: ref, identity: unnamed, kind: .terminal)
        #expect(request.dialogTitle == "Rename Terminal “[3] Shell”")
        #expect(request.draft.isEmpty)
        #expect(!request.canSubmit)
    }

    @Test("Session picker groups rows and marks the current selection")
    func sessionPickerPresentation() throws {
        let connectionID = UUID()
        let workspace = WorkspaceRef(connectionID: connectionID, workspaceID: "wsp")
        let terminal = navigationSession(
            "terminal", index: 1, actionName: nil, isAgent: false
        )
        let agent = navigationSession(
            "agent", index: 2, actionName: "Codex", isAgent: true
        )
        let selected = SessionRef(connectionID: connectionID, sessionID: terminal.id)
        let presentation = SessionPickerPresentation(
            workspace: workspace,
            sessions: [agent, terminal],
            selectedSession: selected
        )

        #expect(presentation.groups.sessions.map(\.ref.sessionID) == ["agent"])
        #expect(presentation.groups.terminals.map(\.ref.sessionID) == ["terminal"])
        #expect(presentation.isSelected(try #require(presentation.groups.terminals.first)))
        #expect(!presentation.isSelected(try #require(presentation.groups.sessions.first)))
    }
}

private func navigationSession(
    _ id: String,
    index: Int?,
    name: String? = nil,
    actionName: String?,
    isAgent: Bool
) -> Session {
    Session(
        id: id,
        sessionIndex: index,
        name: name,
        actionId: actionName.map { _ in "act_\(id)" },
        actionName: actionName,
        isAgent: isAgent,
        workingDir: "/tmp",
        status: .live,
        createdAt: .now,
        updatedAt: .now,
        workspace: SessionWorkspace(id: "wsp", name: "Workspace")
    )
}

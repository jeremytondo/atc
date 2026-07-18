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

    @Test("Workspace Switcher projects no-active, connection, and archive context")
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
        #expect(noActive.label == "Select Workspace…")
        #expect(noActive.help == "Select an Active Workspace")

        let connected = WorkspaceSwitcherPresentation(
            project: project,
            workspace: workspace,
            connectionName: "Mac mini",
            reachability: .connected
        )
        #expect(connected.label == "Shared › Parser")
        #expect(connected.help.contains("Mac mini"))
        #expect(connected.help.contains("Connected"))

        let ambiguous = WorkspaceSwitcherPresentation(
            project: project,
            workspace: workspace,
            connectionName: "Laptop",
            reachability: .unreachable
        )
        #expect(ambiguous.label == connected.label)
        #expect(ambiguous.help != connected.help)
        #expect(ambiguous.help.contains("Disconnected"))

        var archivedWorkspace = workspace
        archivedWorkspace.archivedAt = .now
        let archived = WorkspaceSwitcherPresentation(
            project: project,
            workspace: archivedWorkspace,
            connectionName: "Mac mini",
            reachability: .connected
        )
        #expect(archived.isArchived)
        #expect(archived.help.contains("Archived"))
    }

    @Test("Session rename dialog follows row classification")
    func sessionRenameTitles() {
        #expect(SessionRenamePresentation(kind: .agent).dialogTitle == "Rename Session")
        #expect(SessionRenamePresentation(kind: .terminal).dialogTitle == "Rename Terminal")
        #expect(!SessionRenamePresentation.canSubmit(" \n "))
        #expect(SessionRenamePresentation.canSubmit(" New name "))
        #expect(SessionRenamePresentation.normalizedName("  New name\n") == "New name")
    }
}

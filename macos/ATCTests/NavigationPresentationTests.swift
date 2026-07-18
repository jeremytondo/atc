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

    @Test("Workspace Switcher projects no-active and archive context")
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

        let active = WorkspaceSwitcherPresentation(
            project: project,
            workspace: workspace
        )
        #expect(active.label == "Shared › Parser")
        #expect(active.help == active.label)

        var archivedWorkspace = workspace
        archivedWorkspace.archivedAt = .now
        let archived = WorkspaceSwitcherPresentation(
            project: project,
            workspace: archivedWorkspace
        )
        #expect(archived.help.contains("Archived"))
    }

    @Test("Session rename request starts with the current display name")
    func sessionRenameRequest() {
        let ref = SessionRef(connectionID: UUID(), sessionID: "ses_123")
        var request = SessionRenameRequest(ref: ref, title: "Current name", kind: .agent)

        #expect(request.ref == ref)
        #expect(request.dialogTitle == "Rename Session")
        #expect(request.draft == "Current name")
        #expect(request.canSubmit)

        request.draft = " \n "
        #expect(!request.canSubmit)

        request = SessionRenameRequest(ref: ref, title: "  New name\n", kind: .terminal)
        #expect(request.dialogTitle == "Rename Terminal")
        #expect(request.normalizedName == "New name")
    }
}

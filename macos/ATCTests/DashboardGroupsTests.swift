import Foundation
import Testing
import ATCAPI
@testable import ATC

/// DashboardGroups: ordering, empty projects, session counts, and the
/// local/remote context label.
@Suite("DashboardGroups")
struct DashboardGroupsTests {
    private let connection = ConnectionRecord(
        name: "Workstation", urlString: "http://workstation:7331", token: ""
    )

    private func project(_ id: String) -> Project {
        Project(
            id: id, name: id, workingDir: "/home/dev/\(id)",
            createdAt: .now, updatedAt: .now
        )
    }

    private func workspace(
        _ id: String, project: String, createdAgo: TimeInterval
    ) -> Workspace {
        Workspace(
            id: id, projectId: project, name: id,
            createdAt: Date(timeIntervalSinceNow: -createdAgo),
            updatedAt: .now
        )
    }

    private func session(
        _ id: String, workspace: String, status: SessionStatus
    ) -> Session {
        Session(
            id: id, isAgent: false, workingDir: "/home/dev",
            status: status,
            createdAt: .now, updatedAt: .now,
            workspace: SessionWorkspace(id: workspace, name: workspace)
        )
    }

    private func groups(
        projects: [Project], workspaces: [Workspace], sessions: [Session] = []
    ) -> DashboardGroups {
        DashboardGroups(
            inputs: [DashboardGroups.ConnectionInput(
                connection: connection,
                projects: projects,
                workspaces: workspaces,
                sessions: sessions
            )]
        )
    }

    @Test("workspace rows order newest-created-first within their card")
    func rowOrdering() {
        let result = groups(
            projects: [project("p1")],
            workspaces: [
                workspace("w_old", project: "p1", createdAgo: 300),
                workspace("w_new", project: "p1", createdAgo: 10),
                workspace("w_mid", project: "p1", createdAgo: 100),
            ]
        )
        #expect(result.sections[0].cards[0].rows.map(\.workspace.id) == ["w_new", "w_mid", "w_old"])
    }

    @Test("a project with zero workspaces keeps its card with an empty row list")
    func emptyProject() {
        let result = groups(projects: [project("lonely")], workspaces: [])
        #expect(result.sections[0].cards.count == 1)
        #expect(result.sections[0].cards[0].rows.isEmpty)
        #expect(result.sections[0].cards[0].totalWorkspaceCount == 0)
        #expect(!result.isEmpty)
    }

    @Test("session counts and activity join by workspace")
    func sessionCounts() {
        let result = groups(
            projects: [project("p1")],
            workspaces: [workspace("w1", project: "p1", createdAgo: 10)],
            sessions: [
                session("s1", workspace: "w1", status: .live),
                session("s2", workspace: "w1", status: .ended),
                session("s3", workspace: "other", status: .live),
            ]
        )
        let row = result.sections[0].cards[0].rows[0]
        #expect(row.sessionCount == 2)
        #expect(row.activeSessionCount == 1)
        #expect(row.hasActiveSessions)
    }

    @Test("sections keep connection order and derive the context label")
    func sectionOrderAndContext() {
        let local = ConnectionRecord(name: "Here", urlString: "http://localhost:7331", token: "")
        let remote = ConnectionRecord(name: "There", urlString: "http://box.ts.net:7331", token: "")
        let result = DashboardGroups(
            inputs: [
                DashboardGroups.ConnectionInput(
                    connection: local, projects: [], workspaces: [], sessions: []
                ),
                DashboardGroups.ConnectionInput(
                    connection: remote, projects: [], workspaces: [], sessions: []
                ),
            ]
        )
        #expect(result.sections.map(\.connectionName) == ["Here", "There"])
        #expect(result.sections[0].contextLabel == "Local")
        #expect(result.sections[1].contextLabel == "box.ts.net")
        #expect(result.isEmpty)
    }

    @Test("context label treats loopback addresses as Local")
    func contextLabels() {
        #expect(ConnectionURL.contextLabel(for: "http://127.0.0.1:7331") == "Local")
        #expect(ConnectionURL.contextLabel(for: "http://[::1]:7331") == "Local")
        #expect(ConnectionURL.contextLabel(for: "http://LOCALHOST:7331") == "Local")
        #expect(ConnectionURL.contextLabel(for: "https://work.example.com") == "work.example.com")
    }
}

import Foundation
import Testing
import ATCAPI
@testable import ATC

@MainActor
@Suite("Projects Navigator groups")
struct ProjectsNavigatorGroupsTests {
    private func connection(_ id: String, name: String) -> ConnectionRecord {
        ConnectionRecord(
            id: UUID(uuidString: id)!,
            name: name,
            urlString: "http://\(name.lowercased()):7331",
            token: ""
        )
    }

    private func project(_ id: String, name: String) -> Project {
        Project(
            id: id,
            name: name,
            workingDir: "/tmp/\(id)",
            createdAt: .now,
            updatedAt: .now
        )
    }

    private func workspace(
        _ id: String,
        project: String,
        age: TimeInterval
    ) -> Workspace {
        Workspace(
            id: id,
            projectId: project,
            name: id,
            createdAt: Date(timeIntervalSinceNow: -age),
            updatedAt: .now
        )
    }

    private func input(
        connection: ConnectionRecord,
        projects: [Project],
        workspaces: [Workspace],
        sessions: [Session] = [],
        reachability: Reachability = .connected
    ) -> ProjectsNavigatorGroups.Input {
        .init(
            connection: connection,
            reachability: reachability,
            projects: projects,
            workspaces: workspaces,
            sessions: sessions
        )
    }

    @Test("Ended sessions count as records but are not active")
    func endedSessionsAreNotActive() {
        let c = connection("00000000-0000-0000-0000-000000000001", name: "Alpha")
        let session = Session(
            id: "ended", isAgent: false, workingDir: "/tmp", status: .ended,
            createdAt: .now, updatedAt: .now,
            workspace: SessionWorkspace(id: "workspace", name: "Workspace")
        )
        let result = ProjectsNavigatorGroups(inputs: [input(
            connection: c,
            projects: [project("project", name: "Project")],
            workspaces: [workspace("workspace", project: "project", age: 1)],
            sessions: [session]
        )])
        let row = result.projects[0].workspaces[0]
        #expect(row.sessionCount == 1)
        #expect(row.activeSessionCount == 0)
        #expect(!row.hasActiveSessions)
    }

    @Test("projects sort case-insensitively, then by Connection name and stable identity")
    func projectOrdering() {
        let z = connection("00000000-0000-0000-0000-000000000002", name: "Zulu")
        let a = connection("00000000-0000-0000-0000-000000000001", name: "Alpha")
        let result = ProjectsNavigatorGroups(inputs: [
            input(connection: z, projects: [project("p2", name: "same")], workspaces: []),
            input(connection: a, projects: [
                project("p3", name: "Beta"),
                project("p1", name: "Same")
            ], workspaces: [])
        ])
        #expect(result.projects.map(\.project.id) == ["p3", "p1", "p2"])
        #expect(result.projects.map(\.connectionName) == ["Alpha", "Alpha", "Zulu"])
    }

    @Test("workspaces sort newest-first")
    func workspaceOrdering() {
        let c = connection("00000000-0000-0000-0000-000000000001", name: "Alpha")
        let result = ProjectsNavigatorGroups(inputs: [input(
            connection: c,
            projects: [project("active", name: "Active")],
            workspaces: [
                workspace("old", project: "active", age: 300),
                workspace("new", project: "active", age: 10)
            ]
        )])
        #expect(result.projects.map(\.project.id) == ["active"])
        #expect(result.projects[0].workspaces.map(\.workspace.id) == ["new", "old"])
        #expect(result.projects[0].totalWorkspaceCount == 2)
    }

    @Test("Connection context and reachability project onto same-named Projects")
    func connectionContextProjection() {
        let a = connection("00000000-0000-0000-0000-000000000001", name: "Alpha")
        let b = connection("00000000-0000-0000-0000-000000000002", name: "Beta")
        let result = ProjectsNavigatorGroups(inputs: [
            input(
                connection: a,
                projects: [project("one", name: "Shared")],
                workspaces: [],
                reachability: .connected
            ),
            input(
                connection: b,
                projects: [project("two", name: "Shared")],
                workspaces: [],
                reachability: .unreachable
            )
        ])
        #expect(result.projects.map(\.connectionName) == ["Alpha", "Beta"])
        #expect(result.projects.map(\.reachability) == [.connected, .unreachable])
        #expect(result.projects[0].ref.connectionID != result.projects[1].ref.connectionID)
    }
}

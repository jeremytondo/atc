import Foundation
import Testing
import ATCAPI
@testable import ATC

/// The sidebar's multi-connection grouping rules: connection creation order
/// first, then server order; no fallback bucket — unscoped sessions and
/// sessions of unlisted projects are dropped (Phase 0's server rule
/// guarantees active sessions can't be orphaned by archiving).
@Suite("SidebarGroups")
struct SidebarGroupsTests {
    private let connectionA = ConnectionRecord(name: "Alpha", urlString: "http://a:1", token: "")
    private let connectionB = ConnectionRecord(name: "Beta", urlString: "http://b:1", token: "")

    private func project(_ id: String, name: String, archived: Bool = false) -> Project {
        Project(
            id: id,
            name: name,
            workingDir: "/home/dev/\(name)",
            createdAt: .now,
            updatedAt: .now,
            archivedAt: archived ? .now : nil
        )
    }

    private func session(
        _ id: String,
        project: Project? = nil,
        status: SessionStatus = .running,
        updatedAt: Date = .now,
        archived: Bool = false
    ) -> Session {
        Session(
            id: id,
            action: "claude",
            environment: "host-login-shell",
            workingDir: project?.workingDir ?? "/tmp/loose",
            status: archived ? .terminated : status,
            attachable: !archived && status == .running,
            createdAt: updatedAt,
            updatedAt: updatedAt,
            archivedAt: archived ? .now : nil,
            project: project.map {
                SessionProject(id: $0.id, name: $0.name, workingDir: $0.workingDir, archivedAt: $0.archivedAt)
            }
        )
    }

    private func input(
        _ connection: ConnectionRecord,
        projects: [Project],
        sessions: [Session]
    ) -> SidebarGroups.ConnectionInput {
        SidebarGroups.ConnectionInput(connection: connection, projects: projects, sessions: sessions)
    }

    @Test("aggregation orders by connection creation order, then server order")
    func aggregationOrder() {
        let a1 = project("prj_a1", name: "A1")
        let a2 = project("prj_a2", name: "A2")
        let b1 = project("prj_b1", name: "B1")
        let groups = SidebarGroups(
            inputs: [
                input(connectionA, projects: [a1, a2], sessions: []),
                input(connectionB, projects: [b1], sessions: []),
            ],
            searchText: ""
        )
        #expect(groups.projectRows.map(\.project.id) == ["prj_a1", "prj_a2", "prj_b1"])
        #expect(groups.projectRows.map(\.connectionName) == ["Alpha", "Alpha", "Beta"])
        #expect(groups.projectRows.map(\.ref.connectionID)
            == [connectionA.id, connectionA.id, connectionB.id])
    }

    @Test("row identity is connection-qualified: same project ID on two connections")
    func refIdentity() {
        let sameID = project("prj_same", name: "Same")
        let groups = SidebarGroups(
            inputs: [
                input(connectionA, projects: [sameID], sessions: [session("ses_1", project: sameID)]),
                input(connectionB, projects: [sameID], sessions: [session("ses_1", project: sameID)]),
            ],
            searchText: ""
        )
        #expect(groups.projectRows.count == 2)
        #expect(Set(groups.projectRows.map(\.ref)).count == 2)
        let refA = ProjectRef(connectionID: connectionA.id, projectID: "prj_same")
        let refB = ProjectRef(connectionID: connectionB.id, projectID: "prj_same")
        #expect(groups.sessions(in: refA).map(\.id) == ["ses_1"])
        #expect(groups.sessions(in: refB).map(\.id) == ["ses_1"])
    }

    @Test("unscoped sessions are dropped — no Other Sessions bucket")
    func unscopedDropped() {
        let groups = SidebarGroups(
            inputs: [input(connectionA, projects: [], sessions: [session("ses_loose")])],
            searchText: ""
        )
        #expect(groups.isEmpty)
    }

    @Test("sessions of an unlisted (archived, filter off) project are dropped")
    func hiddenProjectSessionsDropped() {
        let hidden = project("prj_hidden", name: "Hidden", archived: true)
        let visible = project("prj_visible", name: "Visible")
        // Archived filter off: only the active project is in the list.
        let groups = SidebarGroups(
            inputs: [input(
                connectionA,
                projects: [visible],
                sessions: [session("ses_orphan", project: hidden), session("ses_ok", project: visible)]
            )],
            searchText: ""
        )
        #expect(groups.projectRows.map(\.project.id) == ["prj_visible"])
        let visibleRef = ProjectRef(connectionID: connectionA.id, projectID: "prj_visible")
        #expect(groups.sessions(in: visibleRef).map(\.id) == ["ses_ok"])
    }

    @Test("archived project row (filter on) nests its sessions")
    func archivedProjectListedWithSessions() {
        let archived = project("prj_arch", name: "Archived", archived: true)
        // Archived filter on: the server returns the archived project, so it
        // is listed and its sessions nest under it.
        let groups = SidebarGroups(
            inputs: [input(
                connectionA,
                projects: [archived],
                sessions: [session("ses_old", project: archived, status: .terminated, archived: true)]
            )],
            searchText: ""
        )
        let ref = ProjectRef(connectionID: connectionA.id, projectID: "prj_arch")
        #expect(groups.projectRows.map(\.project.id) == ["prj_arch"])
        #expect(groups.sessions(in: ref).map(\.id) == ["ses_old"])
    }

    @Test("archive gating: starting or running sessions flag the row")
    func archiveGating() {
        let active = project("prj_active", name: "Active")
        let startingOnly = project("prj_starting", name: "Starting")
        let done = project("prj_done", name: "Done")
        let empty = project("prj_empty", name: "Empty")
        let groups = SidebarGroups(
            inputs: [input(
                connectionA,
                projects: [active, startingOnly, done, empty],
                sessions: [
                    session("ses_run", project: active, status: .running),
                    session("ses_start", project: startingOnly, status: .starting),
                    session("ses_done", project: done, status: .terminated),
                    session("ses_fail", project: done, status: .failed),
                ]
            )],
            searchText: ""
        )
        let byID = Dictionary(uniqueKeysWithValues: groups.projectRows.map { ($0.project.id, $0) })
        #expect(byID["prj_active"]?.hasActiveSessions == true)
        #expect(byID["prj_starting"]?.hasActiveSessions == true)
        #expect(byID["prj_done"]?.hasActiveSessions == false)
        #expect(byID["prj_empty"]?.hasActiveSessions == false)
    }

    @Test("archive gating sees sessions hidden by search filtering")
    func archiveGatingIgnoresSearch() {
        let prj = project("prj_a", name: "Atelier")
        var named = session("ses_hidden", project: prj, status: .running)
        named.name = "does-not-match-search"
        let groups = SidebarGroups(
            inputs: [input(connectionA, projects: [prj], sessions: [named])],
            searchText: "atelier"
        )
        // Project matches by name; its non-matching session still counts
        // toward the archive gate.
        #expect(groups.projectRows.first?.hasActiveSessions == true)
    }

    @Test("archived sessions trail active ones within a group")
    func archivedTrail() {
        let prj = project("prj_a", name: "A")
        let old = Date(timeIntervalSinceNow: -3600)
        let groups = SidebarGroups(
            inputs: [input(
                connectionA,
                projects: [prj],
                sessions: [
                    session("ses_archived_new", project: prj, updatedAt: .now, archived: true),
                    session("ses_active_old", project: prj, updatedAt: old),
                    session("ses_active_new", project: prj, updatedAt: .now),
                ]
            )],
            searchText: ""
        )
        let ref = ProjectRef(connectionID: connectionA.id, projectID: "prj_a")
        #expect(groups.sessions(in: ref).map(\.id)
            == ["ses_active_new", "ses_active_old", "ses_archived_new"])
    }

    @Test("search: matching project keeps all sessions, non-matching filters")
    func searchRules() {
        let atelier = project("prj_atelier", name: "Atelier")
        let other = project("prj_other", name: "Widgets")
        var named = session("ses_named", project: other)
        named.name = "atelier follow-up"
        let groups = SidebarGroups(
            inputs: [input(
                connectionA,
                projects: [atelier, other],
                sessions: [session("ses_a", project: atelier), named, session("ses_b", project: other)]
            )],
            searchText: "atelier"
        )
        // Atelier matches by name → visible with all sessions; Widgets only
        // survives through its matching session.
        #expect(groups.projectRows.map(\.project.id) == ["prj_atelier", "prj_other"])
        let atelierRef = ProjectRef(connectionID: connectionA.id, projectID: "prj_atelier")
        let otherRef = ProjectRef(connectionID: connectionA.id, projectID: "prj_other")
        #expect(groups.sessions(in: atelierRef).map(\.id) == ["ses_a"])
        #expect(groups.sessions(in: otherRef).map(\.id) == ["ses_named"])
    }

    @Test("search with no matches hides everything")
    func searchNoMatches() {
        let prj = project("prj_a", name: "A")
        let groups = SidebarGroups(
            inputs: [input(connectionA, projects: [prj], sessions: [session("ses_a", project: prj)])],
            searchText: "zzz-no-match"
        )
        #expect(groups.isEmpty)
    }

    @Test("connection name is plumbed onto every row for the chip")
    func chipPlumbing() {
        let groups = SidebarGroups(
            inputs: [
                input(connectionA, projects: [project("prj_1", name: "One")], sessions: []),
                input(connectionB, projects: [project("prj_2", name: "Two")], sessions: []),
            ],
            searchText: ""
        )
        #expect(groups.projectRows.map(\.connectionName) == ["Alpha", "Beta"])
    }
}

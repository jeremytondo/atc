import Foundation
import Testing
import CockpitAPI
@testable import AtelierCode

/// The sidebar's grouping rules, including the reachability guarantee:
/// sessions must never vanish just because their project is filtered out.
@Suite("SidebarGroups")
struct SidebarGroupsTests {
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
        updatedAt: Date = .now,
        archived: Bool = false
    ) -> Session {
        Session(
            id: id,
            action: "claude",
            environment: "host-login-shell",
            workingDir: project?.workingDir ?? "/tmp/loose",
            status: archived ? .terminated : .running,
            attachable: !archived,
            createdAt: updatedAt,
            updatedAt: updatedAt,
            archivedAt: archived ? .now : nil,
            project: project.map {
                SessionProject(id: $0.id, name: $0.name, workingDir: $0.workingDir, archivedAt: $0.archivedAt)
            }
        )
    }

    @Test("sessions of a hidden (archived) project fall into Other Sessions")
    func hiddenProjectSessionsStayReachable() {
        let hidden = project("prj_hidden", name: "Hidden", archived: true)
        let visible = project("prj_visible", name: "Visible")
        // Archived filter off: only the active project is in the list.
        let groups = SidebarGroups(
            projects: [visible],
            sessions: [session("ses_orphan", project: hidden), session("ses_ok", project: visible)],
            searchText: ""
        )
        #expect(groups.visibleProjects.map(\.id) == ["prj_visible"])
        #expect(groups.otherSessions.map(\.id) == ["ses_orphan"])
        #expect(groups.sessions(in: "prj_visible").map(\.id) == ["ses_ok"])
    }

    @Test("unscoped sessions land in Other Sessions")
    func unscopedSessions() {
        let groups = SidebarGroups(projects: [], sessions: [session("ses_loose")], searchText: "")
        #expect(groups.otherSessions.map(\.id) == ["ses_loose"])
        #expect(!groups.isEmpty)
    }

    @Test("archived sessions trail active ones within a group")
    func archivedTrail() {
        let prj = project("prj_a", name: "A")
        let old = Date(timeIntervalSinceNow: -3600)
        let groups = SidebarGroups(
            projects: [prj],
            sessions: [
                session("ses_archived_new", project: prj, updatedAt: .now, archived: true),
                session("ses_active_old", project: prj, updatedAt: old),
                session("ses_active_new", project: prj, updatedAt: .now),
            ],
            searchText: ""
        )
        #expect(groups.sessions(in: "prj_a").map(\.id) == ["ses_active_new", "ses_active_old", "ses_archived_new"])
    }

    @Test("search: matching project keeps all sessions, non-matching filters")
    func searchRules() {
        let atelier = project("prj_atelier", name: "Atelier")
        let other = project("prj_other", name: "Widgets")
        var named = session("ses_named", project: other)
        named.name = "atelier follow-up"
        let groups = SidebarGroups(
            projects: [atelier, other],
            sessions: [session("ses_a", project: atelier), named, session("ses_b", project: other)],
            searchText: "atelier"
        )
        // Atelier matches by name → visible with all sessions; Widgets only
        // survives through its matching session.
        #expect(groups.visibleProjects.map(\.id) == ["prj_atelier", "prj_other"])
        #expect(groups.sessions(in: "prj_atelier").map(\.id) == ["ses_a"])
        #expect(groups.sessions(in: "prj_other").map(\.id) == ["ses_named"])
    }

    @Test("search with no matches hides everything")
    func searchNoMatches() {
        let prj = project("prj_a", name: "A")
        let groups = SidebarGroups(
            projects: [prj],
            sessions: [session("ses_a", project: prj)],
            searchText: "zzz-no-match"
        )
        #expect(groups.isEmpty)
    }
}

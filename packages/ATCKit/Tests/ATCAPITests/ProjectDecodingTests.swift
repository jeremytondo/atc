import Foundation
import Testing
@testable import ATCAPI

/// Fixture shaped like `GET /api/projects` (`{"projects":[...]}`, newest
/// first) with RFC3339Nano dates using varying fraction digits.
private let projectsFixture = Data(#"""
{"projects":[
  {"id":"prj_atelier","name":"Atelier","workingDir":"/home/dev/Projects/atelier","createdAt":"2026-07-01T10:00:00.5Z","updatedAt":"2026-07-05T12:34:56.123456789Z"},
  {"id":"prj_old","name":"Old Thing","workingDir":"/home/dev/Projects/old","createdAt":"2026-06-01T08:00:00Z","updatedAt":"2026-06-15T09:30:00.42Z"}
]}
"""#.utf8)

private let projectFixture = Data(#"""
{"id":"prj_atelier","name":"Atelier","workingDir":"/home/dev/Projects/atelier","createdAt":"2026-07-01T10:00:00.5Z","updatedAt":"2026-07-05T12:34:56.123456789Z"}
"""#.utf8)

/// A session detail carrying nested workspace and derived project references.
private let sessionWithProjectFixture = Data(#"""
{"id":"ses_scoped","action":"claude","environment":"host-login-shell","workingDir":"/home/dev/Projects/atelier","status":"live","createdAt":"2026-07-05T12:00:00Z","updatedAt":"2026-07-05T12:00:00Z","workspace":{"id":"wsp_login","name":"Login bug"},"project":{"id":"prj_atelier","name":"Atelier"}}
"""#.utf8)

/// A session detail with no refs (defensive: the server always sends both).
private let sessionWithoutProjectFixture = Data(#"""
{"id":"ses_unscoped","action":"codex","environment":"host-login-shell","workingDir":"/tmp","status":"live","createdAt":"2026-07-05T12:00:00Z","updatedAt":"2026-07-05T12:00:00Z"}
"""#.utf8)

@Suite("Project decoding")
struct ProjectDecodingTests {
    @Test("wrapped list decodes")
    func listDecodes() throws {
        let envelope = try JSONDecoder.atc().decode(ProjectsEnvelope.self, from: projectsFixture)
        #expect(envelope.projects.count == 2)

        let active = envelope.projects[0]
        #expect(active.id == "prj_atelier")
        #expect(active.name == "Atelier")
        #expect(active.workingDir == "/home/dev/Projects/atelier")
        #expect(envelope.projects[1].id == "prj_old")
    }

    @Test("single project decodes")
    func singleDecodes() throws {
        let project = try JSONDecoder.atc().decode(Project.self, from: projectFixture)
        #expect(project.id == "prj_atelier")
        #expect(project.workingDir == "/home/dev/Projects/atelier")
    }

    @Test("session detail decodes nested workspace and derived project")
    func sessionWithProjectDecodes() throws {
        let detail = try JSONDecoder.atc().decode(SessionDetail.self, from: sessionWithProjectFixture)
        let workspace = try #require(detail.workspace)
        #expect(workspace.id == "wsp_login")
        #expect(workspace.name == "Login bug")
        let project = try #require(detail.project)
        #expect(project.id == "prj_atelier")
        #expect(project.name == "Atelier")
        // The projection carries both refs through.
        #expect(detail.asSession.workspace?.id == "wsp_login")
        #expect(detail.asSession.project?.id == "prj_atelier")
    }

    @Test("session detail without refs decodes to nil")
    func sessionWithoutProjectDecodes() throws {
        let detail = try JSONDecoder.atc().decode(SessionDetail.self, from: sessionWithoutProjectFixture)
        #expect(detail.workspace == nil)
        #expect(detail.project == nil)
    }

    @Test("start request encodes workspaceId and the chosen action")
    func encodesWorkspaceId() throws {
        let request = StartSessionRequest(workspaceId: "wsp_login", action: "claude")
        let json = String(decoding: try JSONEncoder().encode(request), as: UTF8.self)
        #expect(json.contains("\"workspaceId\""))
        #expect(json.contains("wsp_login"))
        #expect(json.contains("\"action\""))
    }

    @Test("start request without action omits it — the Interactive Shell")
    func omitsActionForInteractiveShell() throws {
        let request = StartSessionRequest(workspaceId: "wsp_login")
        let json = String(decoding: try JSONEncoder().encode(request), as: UTF8.self)
        #expect(json.contains("\"workspaceId\""))
        #expect(!json.contains("\"action\""))
    }
}

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

/// A session carrying nested workspace and derived project references.
private let sessionWithProjectFixture = Data(#"""
{"id":"ses_scoped","actionId":"act_vpj2tlg9viqd8ms52ptuvao5c4","actionName":"Claude","isAgent":true,"workingDir":"/home/dev/Projects/atelier","status":"live","createdAt":"2026-07-05T12:00:00Z","updatedAt":"2026-07-05T12:00:00Z","workspace":{"id":"wsp_login","name":"Login bug"},"project":{"id":"prj_atelier","name":"Atelier"}}
"""#.utf8)

/// A session with no refs (defensive: the server always sends both).
private let sessionWithoutProjectFixture = Data(#"""
{"id":"ses_unscoped","actionId":"act_fh9g7e6571qo53r0t647ughtfg","actionName":"Codex","isAgent":true,"workingDir":"/tmp","status":"live","createdAt":"2026-07-05T12:00:00Z","updatedAt":"2026-07-05T12:00:00Z"}
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

    @Test("session decodes nested workspace and derived project")
    func sessionWithProjectDecodes() throws {
        let session = try JSONDecoder.atc().decode(Session.self, from: sessionWithProjectFixture)
        let workspace = try #require(session.workspace)
        #expect(workspace.id == "wsp_login")
        #expect(workspace.name == "Login bug")
        let project = try #require(session.project)
        #expect(project.id == "prj_atelier")
        #expect(project.name == "Atelier")
    }

    @Test("session without refs decodes to nil")
    func sessionWithoutProjectDecodes() throws {
        let session = try JSONDecoder.atc().decode(Session.self, from: sessionWithoutProjectFixture)
        #expect(session.workspace == nil)
        #expect(session.project == nil)
    }

    @Test("start request encodes workspaceId and the chosen action id")
    func encodesWorkspaceId() throws {
        let request = StartSessionRequest(
            workspaceId: "wsp_login",
            actionId: "act_vpj2tlg9viqd8ms52ptuvao5c4"
        )
        let json = String(decoding: try JSONEncoder().encode(request), as: UTF8.self)
        #expect(json.contains("\"workspaceId\""))
        #expect(json.contains("wsp_login"))
        #expect(json.contains("\"actionId\""))
    }

    @Test("start request without action id omits it — the Interactive Shell")
    func omitsActionForInteractiveShell() throws {
        let request = StartSessionRequest(workspaceId: "wsp_login")
        let json = String(decoding: try JSONEncoder().encode(request), as: UTF8.self)
        #expect(json.contains("\"workspaceId\""))
        #expect(!json.contains("\"actionId\""))
    }
}

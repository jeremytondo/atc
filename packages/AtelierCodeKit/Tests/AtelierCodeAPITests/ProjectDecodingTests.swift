import Foundation
import Testing
@testable import AtelierCodeAPI

/// Fixture shaped like `GET /api/projects?includeArchived=true`
/// (`{"projects":[...]}`, newest first). One archived, RFC3339Nano dates
/// with varying fraction digits.
private let projectsFixture = Data(#"""
{"projects":[
  {"id":"prj_atelier","name":"Atelier","workingDir":"/home/dev/Projects/atelier","createdAt":"2026-07-01T10:00:00.5Z","updatedAt":"2026-07-05T12:34:56.123456789Z"},
  {"id":"prj_old","name":"Old Thing","workingDir":"/home/dev/Projects/old","createdAt":"2026-06-01T08:00:00Z","updatedAt":"2026-06-15T09:30:00.42Z","archivedAt":"2026-06-20T18:00:00.987654321Z"}
]}
"""#.utf8)

private let projectFixture = Data(#"""
{"id":"prj_atelier","name":"Atelier","workingDir":"/home/dev/Projects/atelier","createdAt":"2026-07-01T10:00:00.5Z","updatedAt":"2026-07-05T12:34:56.123456789Z"}
"""#.utf8)

/// A session detail carrying a nested project reference.
private let sessionWithProjectFixture = Data(#"""
{"id":"ses_scoped","action":"claude","environment":"host-login-shell","workingDir":"/home/dev/Projects/atelier","status":"running","attachable":true,"createdAt":"2026-07-05T12:00:00Z","updatedAt":"2026-07-05T12:00:00Z","project":{"id":"prj_atelier","name":"Atelier","workingDir":"/home/dev/Projects/atelier"}}
"""#.utf8)

/// A session detail with no project (legacy/unscoped).
private let sessionWithoutProjectFixture = Data(#"""
{"id":"ses_unscoped","action":"codex","environment":"host-login-shell","workingDir":"/tmp","status":"running","attachable":true,"createdAt":"2026-07-05T12:00:00Z","updatedAt":"2026-07-05T12:00:00Z"}
"""#.utf8)

@Suite("Project decoding")
struct ProjectDecodingTests {
    @Test("wrapped list decodes; archived project carries archivedAt")
    func listDecodes() throws {
        let envelope = try JSONDecoder.atelierCode().decode(ProjectsEnvelope.self, from: projectsFixture)
        #expect(envelope.projects.count == 2)

        let active = envelope.projects[0]
        #expect(active.id == "prj_atelier")
        #expect(active.name == "Atelier")
        #expect(active.workingDir == "/home/dev/Projects/atelier")
        #expect(!active.isArchived)
        #expect(active.archivedAt == nil)

        let archived = envelope.projects[1]
        #expect(archived.isArchived)
        #expect(archived.archivedAt != nil)
    }

    @Test("single project decodes")
    func singleDecodes() throws {
        let project = try JSONDecoder.atelierCode().decode(Project.self, from: projectFixture)
        #expect(project.id == "prj_atelier")
        #expect(project.workingDir == "/home/dev/Projects/atelier")
        #expect(!project.isArchived)
    }

    @Test("session detail decodes nested project")
    func sessionWithProjectDecodes() throws {
        let detail = try JSONDecoder.atelierCode().decode(SessionDetail.self, from: sessionWithProjectFixture)
        let project = try #require(detail.project)
        #expect(project.id == "prj_atelier")
        #expect(project.name == "Atelier")
        #expect(project.workingDir == "/home/dev/Projects/atelier")
        #expect(project.archivedAt == nil)
        // The projection carries the project through.
        #expect(detail.asSession.project?.id == "prj_atelier")
    }

    @Test("session detail without project decodes to nil")
    func sessionWithoutProjectDecodes() throws {
        let detail = try JSONDecoder.atelierCode().decode(SessionDetail.self, from: sessionWithoutProjectFixture)
        #expect(detail.project == nil)
    }

    @Test("start request with projectId encodes projectId and omits workingDir")
    func encodesProjectId() throws {
        let request = StartSessionRequest(action: "claude", projectId: "prj_atelier")
        let json = String(decoding: try JSONEncoder().encode(request), as: UTF8.self)
        #expect(json.contains("\"projectId\""))
        #expect(json.contains("prj_atelier"))
        #expect(!json.contains("\"workingDir\""))
    }

    @Test("start request with workingDir encodes workingDir and omits projectId")
    func encodesWorkingDir() throws {
        let request = StartSessionRequest(action: "claude", workingDir: "/tmp")
        let json = String(decoding: try JSONEncoder().encode(request), as: UTF8.self)
        #expect(json.contains("\"workingDir\""))
        #expect(json.contains("/tmp"))
        #expect(!json.contains("\"projectId\""))
    }
}

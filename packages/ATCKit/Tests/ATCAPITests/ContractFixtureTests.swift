import Foundation
import Testing
@testable import ATCAPI

/// Decodes the shared cross-client fixtures in packages/contracts/fixtures —
/// the same files the Go server round-trips and the web client type-checks —
/// so a wire-shape change breaks this suite instead of the app at runtime.
@Suite("Contract fixtures decode into Kit models")
struct ContractFixtureTests {
    private func fixtureData(_ name: String) throws -> Data {
        let url = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent() // ATCAPITests
            .deletingLastPathComponent() // Tests
            .deletingLastPathComponent() // ATCKit
            .deletingLastPathComponent() // packages
            .appendingPathComponent("contracts/fixtures/\(name).json")
        return try Data(contentsOf: url)
    }

    /// Wrapper matching the fixture file layout; only `response` is decoded.
    private struct Fixture<Body: Decodable>: Decodable {
        let response: Body
    }

    private func decodeResponse<Body: Decodable>(_ name: String, as type: Body.Type) throws -> Body {
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .atcRFC3339Nano
        return try decoder.decode(Fixture<Body>.self, from: fixtureData(name)).response
    }

    @Test("sessions list")
    func sessionsList() throws {
        let sessions = try decodeResponse("sessions-list", as: SessionsEnvelope.self).sessions
        #expect(sessions.count == 2)
        #expect(sessions[0].status == .running)
        #expect(sessions[0].project?.id == "prj_fixture01")
        #expect(sessions[1].status == .failed)
        #expect(sessions[1].failureCode == "launch_failed")
        #expect(sessions[1].isArchived)
    }

    @Test("session detail", arguments: ["session-detail", "session-start"])
    func sessionDetail(fixture: String) throws {
        let detail = try decodeResponse(fixture, as: SessionDetail.self)
        #expect(!detail.id.isEmpty)
        #expect(detail.project != nil)
    }

    @Test("projects list")
    func projectsList() throws {
        let projects = try decodeResponse("projects-list", as: ProjectsEnvelope.self).projects
        #expect(projects.count == 2)
        #expect(!projects[0].isArchived)
        #expect(projects[1].isArchived)
    }

    @Test("project detail", arguments: ["project-detail", "project-create", "project-rename"])
    func projectDetail(fixture: String) throws {
        let project = try decodeResponse(fixture, as: Project.self)
        #expect(!project.id.isEmpty)
        #expect(!project.workingDir.isEmpty)
    }

    @Test("actions list")
    func actionsList() throws {
        let actions = try decodeResponse("actions-list", as: ActionsEnvelope.self).actions
        #expect(actions.count == 2)
        #expect(actions[0].isModified)
        #expect(actions[0].acceptsPrompt)
        #expect(actions[0].params["model"]?.isEnum == true)
        #expect(actions[1].isBuiltin)
        #expect(!actions[1].enabled)
    }

    @Test("action detail", arguments: ["action-detail", "action-create", "action-update", "action-enabled"])
    func actionDetail(fixture: String) throws {
        let action = try decodeResponse(fixture, as: ATCAction.self)
        #expect(!action.name.isEmpty)
        #expect(action.command != nil)
    }

    @Test("environments")
    func environments() throws {
        let environments = try decodeResponse("environments", as: EnvironmentsEnvelope.self).environments
        #expect(environments.count == 2)
        #expect(environments[0].isDefault)
        #expect(!environments[1].isDefault)
    }

    @Test("fs list")
    func fsList() throws {
        let listing = try decodeResponse("fs-list", as: DirectoryListing.self)
        #expect(listing.entries.count == 3)
        #expect(listing.entries[0].kind == .directory)
        #expect(listing.entries[1].size == 2048)
        #expect(listing.entries[2].kind == .unknown)
    }

    @Test("error envelope")
    func errorEnvelope() throws {
        let envelope = try decodeResponse("error", as: ErrorEnvelope.self)
        #expect(envelope.error == "session_not_found")
    }

    @Test("health and version")
    func diagnostics() throws {
        #expect(try decodeResponse("health", as: Health.self).status == "ok")
        #expect(try decodeResponse("version", as: Version.self).name == "atc")
    }
}

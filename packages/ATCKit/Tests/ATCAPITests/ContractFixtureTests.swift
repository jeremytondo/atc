import Foundation
import Testing
@testable import ATCAPI

/// Decodes the shared cross-client fixtures in packages/contracts/fixtures.
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

    private struct Fixture<Body: Decodable>: Decodable {
        let response: Body
    }

    private struct EmptyResponse: Decodable {}

    private func decodeResponse<Body: Decodable>(_ name: String, as type: Body.Type) throws -> Body {
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .atcRFC3339Nano
        return try decoder.decode(Fixture<Body>.self, from: fixtureData(name)).response
    }

    @Test("sessions list")
    func sessionsList() throws {
        let sessions = try decodeResponse("sessions-list", as: SessionsEnvelope.self).sessions
        #expect(sessions.count == 1)
        #expect(sessions[0].status == .live)
        #expect(sessions[0].actionName == "Claude")
        #expect(sessions[0].isAgent)
        #expect(sessions[0].workspace?.id == "wsp_fixture01")
        #expect(sessions[0].project?.id == "prj_fixture01")
    }

    @Test("session response", arguments: ["session-detail", "session-start", "session-rename"])
    func session(fixture: String) throws {
        let session = try decodeResponse(fixture, as: Session.self)
        #expect(!session.id.isEmpty)
        #expect(session.workspace != nil)
        #expect(session.project != nil)
        if fixture == "session-detail" {
            #expect(session.actionId == nil)
            #expect(session.actionName == nil)
            #expect(!session.isAgent)
        }
    }

    @Test(
        "empty session response",
        arguments: ["session-delete", "session-send-key", "session-send-text"]
    )
    func emptySessionResponse(fixture: String) throws {
        _ = try decodeResponse(fixture, as: EmptyResponse.self)
    }

    @Test("workspace sessions list")
    func workspaceSessions() throws {
        let sessions = try decodeResponse("workspace-sessions", as: SessionsEnvelope.self).sessions
        #expect(sessions.count == 1)
        #expect(sessions[0].workspace?.id == "wsp_fixture01")
    }

    @Test("projects list")
    func projectsList() throws {
        let projects = try decodeResponse("projects-list", as: ProjectsEnvelope.self).projects
        #expect(projects.count == 1)
    }

    @Test("project detail", arguments: ["project-detail", "project-create", "project-rename"])
    func projectDetail(fixture: String) throws {
        let project = try decodeResponse(fixture, as: Project.self)
        #expect(!project.id.isEmpty)
        #expect(!project.workingDir.isEmpty)
    }

    @Test("workspaces list")
    func workspacesList() throws {
        let workspaces = try decodeResponse("workspaces-list", as: WorkspacesEnvelope.self).workspaces
        #expect(workspaces.count == 1)
        #expect(workspaces[0].projectId == "prj_fixture01")
    }

    @Test("workspace detail", arguments: ["workspace-detail", "workspace-create", "workspace-rename"])
    func workspaceDetail(fixture: String) throws {
        let workspace = try decodeResponse(fixture, as: Workspace.self)
        #expect(!workspace.id.isEmpty)
        #expect(!workspace.projectId.isEmpty)
        #expect(!workspace.name.isEmpty)
    }

    @Test("actions list")
    func actionsList() throws {
        let actions = try decodeResponse("actions-list", as: ActionsEnvelope.self).actions
        #expect(actions.count == 2)
        #expect(actions[0].isAgent)
        #expect(actions[0].description != nil)
        #expect(actions[0].args.isEmpty)
        #expect(!actions[1].isAgent)
        #expect(actions[1].description == nil)
        #expect(!actions[1].enabled)
        #expect(actions[1].args == ["run", "dev"])
    }

    @Test("action response", arguments: ["action-detail", "action-create", "action-update"])
    func action(fixture: String) throws {
        let action = try decodeResponse(fixture, as: ATCAction.self)
        #expect(action.id.hasPrefix("act_"))
        #expect(!action.name.isEmpty)
        #expect(!action.command.isEmpty)
    }

    @Test("action delete response")
    func actionDelete() throws {
        _ = try decodeResponse("action-delete", as: EmptyResponse.self)
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
        #expect(envelope.sessionId == "ses_fixture01")
    }

    @Test("health and version")
    func diagnostics() throws {
        #expect(try decodeResponse("health", as: Health.self).status == "ok")
        #expect(try decodeResponse("version", as: Version.self).name == "atc")
    }
}

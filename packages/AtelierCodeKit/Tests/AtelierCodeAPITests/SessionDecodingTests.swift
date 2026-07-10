import Foundation
import Testing
@testable import AtelierCodeAPI

/// Fixture captured from the live server on 2026-07-03
/// (`GET /api/sessions?includeArchived=true`).
private let sessionsFixture = Data(#"""
{"sessions":[
  {"id":"ses_p6p7ma7hu7gs7gf6us9k97434g","action":"claude","environment":"host-login-shell","workingDir":"/home/jeremytondo/Projects/AtelierCode","status":"terminated","attachable":false,"createdAt":"2026-06-30T03:30:53.536616153Z","updatedAt":"2026-07-01T20:09:31.517106561Z","terminatedAt":"2026-07-01T19:29:02.141995752Z","archivedAt":"2026-07-01T20:09:31.517106561Z"},
  {"id":"ses_n7ka7ajhjlt0hfi0n466aa5mg4","name":"Atelier Code Claude Test","action":"claude","environment":"host-login-shell","workingDir":"/home/jeremytondo/Projects/AtelierCode/server","status":"terminated","attachable":false,"createdAt":"2026-06-30T02:59:17.697575859Z","updatedAt":"2026-07-01T19:29:02.16375741Z","terminatedAt":"2026-07-01T19:29:02.16375741Z"},
  {"id":"ses_live","name":"Live One","action":"codex","environment":"host-login-shell","workingDir":"/tmp","status":"running","attachable":true,"createdAt":"2026-06-30T01:44:53.678613522Z","updatedAt":"2026-06-30T01:44:53.678613522Z"},
  {"id":"ses_failed","action":"codex","environment":"host-login-shell","workingDir":"/tmp","status":"failed","attachable":false,"failureReason":"command not found","failureCode":"spawn_failed","createdAt":"2026-06-30T01:43:06.504785282Z","updatedAt":"2026-06-30T01:44:28.228109473Z"}
]}
"""#.utf8)

private let detailFixture = Data(#"""
{"id":"ses_live","name":"Live One","action":"codex","environment":"host-login-shell","params":{"model":"gpt-5","yolo":true},"workingDir":"/tmp","prompt":"do the thing","status":"running","attachable":true,"createdAt":"2026-06-30T01:44:53.678613522Z","updatedAt":"2026-06-30T01:44:53.678613522Z"}
"""#.utf8)

@Suite("Session decoding")
struct SessionDecodingTests {
    @Test("wrapped list decodes with real server shapes")
    func listDecodes() throws {
        let envelope = try JSONDecoder.atelierCode().decode(SessionsEnvelope.self, from: sessionsFixture)
        #expect(envelope.sessions.count == 4)

        let archived = envelope.sessions[0]
        #expect(archived.isArchived)
        #expect(archived.status == .terminated)
        #expect(archived.name == nil)
        #expect(archived.displayName == "claude")

        let named = envelope.sessions[1]
        #expect(!named.isArchived)
        #expect(named.displayName == "Atelier Code Claude Test")
        #expect(named.terminatedAt != nil)

        let live = envelope.sessions[2]
        #expect(live.status == .running)
        #expect(live.attachable)

        let failed = envelope.sessions[3]
        #expect(failed.status == .failed)
        #expect(failed.failureCode == "spawn_failed")
    }

    @Test("detail decodes params and prompt")
    func detailDecodes() throws {
        let detail = try JSONDecoder.atelierCode().decode(SessionDetail.self, from: detailFixture)
        #expect(detail.prompt == "do the thing")
        #expect(detail.params?["model"] == .string("gpt-5"))
        #expect(detail.params?["yolo"] == .bool(true))
        #expect(detail.asSession.id == detail.id)
        #expect(detail.asSession.status == .running)
    }
}

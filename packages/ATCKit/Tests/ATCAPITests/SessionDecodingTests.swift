import Foundation
import Testing
@testable import ATCAPI

/// Representative fixtures for the closed Live/Ended session contract.
private let sessionsFixture = Data(#"""
{"sessions":[
  {"id":"ses_ended","action":"claude","environment":"host-login-shell","workingDir":"/tmp","status":"ended","createdAt":"2026-06-30T03:30:53.536616153Z","updatedAt":"2026-07-01T20:09:31.517106561Z"},
  {"id":"ses_live","name":"Live One","action":"codex","environment":"host-login-shell","workingDir":"/tmp","status":"live","createdAt":"2026-06-30T01:44:53.678613522Z","updatedAt":"2026-06-30T01:44:53.678613522Z"},
  {"id":"ses_shell","environment":"host-login-shell","workingDir":"/tmp","status":"live","createdAt":"2026-06-30T01:45:00Z","updatedAt":"2026-06-30T01:45:00Z","workspace":{"id":"wsp_shell","name":"Scratch"},"project":{"id":"prj_tmp","name":"Tmp"}}
]}
"""#.utf8)

private let detailFixture = Data(#"""
{"id":"ses_live","name":"Live One","action":"codex","environment":"host-login-shell","params":{"model":"gpt-5","yolo":true},"workingDir":"/tmp","prompt":"do the thing","status":"live","createdAt":"2026-06-30T01:44:53.678613522Z","updatedAt":"2026-06-30T01:44:53.678613522Z"}
"""#.utf8)

@Suite("Session decoding")
struct SessionDecodingTests {
    @Test("wrapped list decodes with real server shapes")
    func listDecodes() throws {
        let envelope = try JSONDecoder.atc().decode(SessionsEnvelope.self, from: sessionsFixture)
        #expect(envelope.sessions.count == 3)

        let ended = envelope.sessions[0]
        #expect(ended.status == .ended)
        #expect(ended.name == nil)
        #expect(ended.displayName == "claude")

        let live = envelope.sessions[1]
        #expect(live.status == .live)

        // An Interactive Shell session has no action and carries its refs.
        let shell = envelope.sessions[2]
        #expect(shell.action == nil)
        #expect(shell.displayName == "Shell")
        #expect(shell.workspace?.id == "wsp_shell")
        #expect(shell.project?.id == "prj_tmp")
    }

    @Test("detail decodes params and prompt")
    func detailDecodes() throws {
        let detail = try JSONDecoder.atc().decode(SessionDetail.self, from: detailFixture)
        #expect(detail.prompt == "do the thing")
        #expect(detail.params?["model"] == .string("gpt-5"))
        #expect(detail.params?["yolo"] == .bool(true))
        #expect(detail.asSession.id == detail.id)
        #expect(detail.asSession.status == .live)
    }

    @Test("legacy statuses do not decode", arguments: ["starting", "running", "failed", "terminated"])
    func legacyStatusDoesNotDecode(status: String) {
        let body = Data("\"\(status)\"".utf8)
        #expect(throws: DecodingError.self) {
            try JSONDecoder.atc().decode(SessionStatus.self, from: body)
        }
    }
}

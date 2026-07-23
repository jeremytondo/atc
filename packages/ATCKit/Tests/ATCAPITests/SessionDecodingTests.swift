import Foundation
import Testing
@testable import ATCAPI

private let sessionsFixture = Data(#"""
{"sessions":[
  {"id":"ses_ended","sessionIndex":1,"actionId":"act_vpj2tlg9viqd8ms52ptuvao5c4","actionName":"Claude","isAgent":true,"workingDir":"/tmp","status":"ended","createdAt":"2026-06-30T03:30:53.536616153Z","updatedAt":"2026-07-01T20:09:31.517106561Z"},
  {"id":"ses_live","sessionIndex":2,"name":"Live One","actionId":"act_editor123456789abcdefghijkl","actionName":"Neovim","isAgent":false,"workingDir":"/tmp","status":"live","createdAt":"2026-06-30T01:44:53.678613522Z","updatedAt":"2026-06-30T01:44:53.678613522Z"},
  {"id":"ses_shell","isAgent":false,"workingDir":"/tmp","status":"live","createdAt":"2026-06-30T01:45:00Z","updatedAt":"2026-06-30T01:45:00Z","workspace":{"id":"wsp_shell","name":"Scratch"},"project":{"id":"prj_tmp","name":"Tmp"}}
]}
"""#.utf8)

@Suite("Session decoding")
struct SessionDecodingTests {
    @Test("all session endpoints share one shape")
    func sessionsDecode() throws {
        let envelope = try JSONDecoder.atc().decode(SessionsEnvelope.self, from: sessionsFixture)
        #expect(envelope.sessions.count == 3)

        let ended = envelope.sessions[0]
        #expect(ended.status == .ended)
        #expect(ended.sessionIndex == 1)
        #expect(ended.name == nil)
        #expect(ended.actionId == "act_vpj2tlg9viqd8ms52ptuvao5c4")
        #expect(ended.actionName == "Claude")
        #expect(ended.isAgent)
        #expect(ended.displayName == "Claude")

        let live = envelope.sessions[1]
        #expect(live.status == .live)
        #expect(live.sessionIndex == 2)
        #expect(!live.isAgent)
        #expect(live.displayName == "Live One")

        let shell = envelope.sessions[2]
        #expect(shell.actionId == nil)
        #expect(shell.sessionIndex == nil)
        #expect(shell.actionName == nil)
        #expect(!shell.isAgent)
        #expect(shell.displayName == "Shell")
        #expect(shell.workspace?.id == "wsp_shell")
        #expect(shell.project?.id == "prj_tmp")
    }

    @Test("isAgent is required by the session contract")
    func missingIsAgentDoesNotDecode() {
        let body = Data(#"""
        {"id":"ses_bad","workingDir":"/tmp","status":"live","createdAt":"2026-06-30T01:45:00Z","updatedAt":"2026-06-30T01:45:00Z"}
        """#.utf8)
        #expect(throws: DecodingError.self) {
            try JSONDecoder.atc().decode(Session.self, from: body)
        }
    }

    @Test("legacy statuses do not decode", arguments: ["starting", "running", "failed", "terminated"])
    func legacyStatusDoesNotDecode(status: String) {
        let body = Data("\"\(status)\"".utf8)
        #expect(throws: DecodingError.self) {
            try JSONDecoder.atc().decode(SessionStatus.self, from: body)
        }
    }
}

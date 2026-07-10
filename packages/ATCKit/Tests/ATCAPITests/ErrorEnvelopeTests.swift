import Foundation
import Testing
@testable import ATCAPI

@Suite("Error envelope")
struct ErrorEnvelopeTests {
    @Test("real 404 body from the live server")
    func notFound() throws {
        let body = Data(#"{"error":"session_not_found","message":"session not found: session not found: nonexistent"}"#.utf8)
        let envelope = try JSONDecoder.atc().decode(ErrorEnvelope.self, from: body)
        #expect(envelope.error == "session_not_found")
        #expect(envelope.sessionId == nil)
    }

    @Test("envelope with sessionId")
    func withSessionID() throws {
        let body = Data(#"{"error":"session_live","message":"session is still running","sessionId":"ses_abc"}"#.utf8)
        let envelope = try JSONDecoder.atc().decode(ErrorEnvelope.self, from: body)
        #expect(envelope.error == "session_live")
        #expect(envelope.sessionId == "ses_abc")
    }

    @Test("ATCError surfaces message and code")
    func errorSurface() {
        let error = ATCError.api(code: "session_live", message: "still running", sessionID: nil)
        #expect(error.apiCode == "session_live")
        #expect(error.errorDescription == "still running")
    }
}

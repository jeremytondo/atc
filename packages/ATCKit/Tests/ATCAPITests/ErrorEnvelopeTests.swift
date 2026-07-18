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
        let body = Data(#"{"error":"session_ended","message":"session has ended","sessionId":"ses_abc"}"#.utf8)
        let envelope = try JSONDecoder.atc().decode(ErrorEnvelope.self, from: body)
        #expect(envelope.error == "session_ended")
        #expect(envelope.sessionId == "ses_abc")
    }

    @Test("ATCError surfaces message and code")
    func errorSurface() {
        let error = ATCError.api(code: "session_ended", message: "session has ended", sessionID: "ses_abc")
        #expect(error.apiCode == "session_ended")
        #expect(error.sessionID == "ses_abc")
        #expect(error.errorDescription == "session has ended")
    }
}

import Foundation
import Testing
@testable import CockpitAPI

@Suite("CockpitServer URL building")
struct CockpitServerTests {
    let server = CockpitServer(baseURL: URL(string: "http://workstation.tail1f9a09.ts.net:7331")!)

    @Test("REST URLs live under /api")
    func restURL() {
        #expect(server.restURL("sessions").absoluteString == "http://workstation.tail1f9a09.ts.net:7331/api/sessions")
        let filtered = server.restURL("sessions", query: [URLQueryItem(name: "includeArchived", value: "true")])
        #expect(filtered.absoluteString == "http://workstation.tail1f9a09.ts.net:7331/api/sessions?includeArchived=true")
    }

    @Test("attach URL swaps scheme to ws")
    func attachURL() {
        #expect(server.attachURL(sessionID: "ses_abc").absoluteString == "ws://workstation.tail1f9a09.ts.net:7331/api/sessions/ses_abc/attach")
        let tls = CockpitServer(baseURL: URL(string: "https://example.com")!)
        #expect(tls.attachURL(sessionID: "x").scheme == "wss")
    }

    @Test("auth headers only when token set")
    func authHeaders() {
        #expect(server.authHeaders.isEmpty)
        let withToken = CockpitServer(baseURL: server.baseURL, token: "sekrit")
        #expect(withToken.authHeaders == ["Authorization": "Bearer sekrit"])
        let emptyToken = CockpitServer(baseURL: server.baseURL, token: "")
        #expect(emptyToken.authHeaders.isEmpty)
    }
}

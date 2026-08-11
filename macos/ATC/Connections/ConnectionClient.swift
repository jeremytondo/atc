// Client construction for a Connection. The empty-token rule lives here and
// nowhere else: a stored `token` of "" means the Connection has no bearer
// token, and sending an empty bearer header instead would be a different
// (and wrong) request.

import ATCAppServerAPI
import Foundation

enum ConnectionClient {
    static func make(baseURL: URL, token: String) -> any APIProtocol {
        ATCAppServerAPI.makeClient(
            baseURL: baseURL,
            bearerToken: token.isEmpty ? nil : token
        )
    }
}

/// The Settings "Test Connection" probe: reports the server version a draft's
/// URL and token reach. Injectable so the editor's states are testable
/// without a server.
struct ConnectionProbe {
    var version: (URL, String) async throws -> String

    static let live = ConnectionProbe { url, token in
        let client = ConnectionClient.make(baseURL: url, token: token)
        _ = try await client.getHealth().ok.body.json
        return try await client.getVersion().ok.body.json.version
    }
}

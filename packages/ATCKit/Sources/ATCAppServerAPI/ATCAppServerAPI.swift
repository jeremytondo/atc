// ATCAppServerAPI: the typed client for the ATC App Server HTTP contract.
//
// `Client`, `Operations`, and `Components` are generated at build time by the
// Swift OpenAPI Generator plugin from `openapi.json` in this directory — a
// symlink to the canonical checked-in artifact at `app-server/openapi.json`,
// which is itself generated from the App Server's `HttpApi` contract module
// (`mise run app-server:openapi`). Nothing in this target is written by hand
// except this file.
//
// Construct a client with the URLSession transport and the ATC date
// transcoder (the runtime's default `.iso8601` cannot parse the server's
// fractional-second timestamps):
//
//     import OpenAPIURLSession
//
//     let client = Client(
//         serverURL: try Servers.Server1.url(),
//         configuration: Configuration(dateTranscoder: ATCDateTranscoder()),
//         transport: URLSessionTransport()
//     )
//     let health = try await client.getHealth().ok.body.json

import Foundation
import OpenAPIRuntime

/// Transcodes the App Server's `format: date-time` fields. The server emits
/// `Date.toISOString()` output — RFC 3339 UTC with exactly three fractional
/// digits — which the runtime's strict `.iso8601` transcoder rejects. Decoding
/// is lenient (fractional seconds optional) so hand-written fixtures and any
/// future whole-second producer parse too; encoding always writes fractional
/// seconds, matching the server byte-for-byte.
public struct ATCDateTranscoder: DateTranscoder {
    private let fractional = Date.ISO8601FormatStyle(includingFractionalSeconds: true)
    private let wholeSecond = Date.ISO8601FormatStyle()

    public init() {}

    public func encode(_ date: Date) throws -> String {
        fractional.format(date)
    }

    public func decode(_ dateString: String) throws -> Date {
        if let date = try? fractional.parse(dateString) {
            return date
        }
        if let date = try? wholeSecond.parse(dateString) {
            return date
        }
        throw DecodingError.dataCorrupted(
            .init(codingPath: [], debugDescription: "Expected an RFC 3339 date-time, got: \(dateString)")
        )
    }
}

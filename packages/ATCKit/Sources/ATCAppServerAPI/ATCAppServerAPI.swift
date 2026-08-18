// ATCAppServerAPI: the typed client for the ATC App Server HTTP contract.
//
// `Client`, `Operations`, and `Components` are generated at build time by the
// Swift OpenAPI Generator plugin from `openapi.json` in this directory — a
// symlink to the canonical checked-in artifact at `app-server/openapi.json`,
// which is itself generated from the App Server's `HttpApi` contract module
// (`mise run app-server:openapi`). Nothing in this target is written by hand
// except this file.
//
// Construct a client through `makeClient(baseURL:bearerToken:)`, which wires
// the URLSession transport, the ATC date transcoder (the runtime's default
// `.iso8601` cannot parse the server's fractional-second timestamps), and —
// when a token is supplied — the bearer-auth middleware that future remote
// servers will require (the local server ignores the header):
//
//     let client = ATCAppServerAPI.makeClient(baseURL: url, bearerToken: nil)
//     let health = try await client.getHealth().ok.body.json

import Foundation
import HTTPTypes
import OpenAPIRuntime
import OpenAPIURLSession

/// The documented construction path for a `Client`: URLSession transport,
/// ATC date transcoder, optional additive bearer auth.
public func makeClient(baseURL: URL, bearerToken: String?) -> Client {
    Client(
        serverURL: baseURL,
        configuration: Configuration(dateTranscoder: ATCDateTranscoder()),
        transport: URLSessionTransport(),
        middlewares: bearerToken.map { [BearerAuthMiddleware(token: $0)] } ?? []
    )
}

/// The decoder for App Server JSON that reaches a client outside the
/// generated operations — the SSE frames of the event streams. It decodes
/// `format: date-time` fields exactly as `makeClient` does; a bare
/// `JSONDecoder()` rejects every payload carrying a timestamp (request and
/// turn events), and a stream that drops what it cannot decode would lose
/// them silently.
public func makeJSONDecoder() -> JSONDecoder {
    let decoder = JSONDecoder()
    let transcoder = ATCDateTranscoder()
    decoder.dateDecodingStrategy = .custom { decoder in
        try transcoder.decode(try decoder.singleValueContainer().decode(String.self))
    }
    return decoder
}

/// Adds `Authorization: Bearer <token>` to every request. Loopback servers
/// ignore it today; remote bearer-token access lands additively on this seam.
public struct BearerAuthMiddleware: ClientMiddleware {
    private let token: String

    public init(token: String) {
        self.token = token
    }

    public func intercept(
        _ request: HTTPRequest,
        body: HTTPBody?,
        baseURL: URL,
        operationID: String,
        next: (HTTPRequest, HTTPBody?, URL) async throws -> (HTTPResponse, HTTPBody?)
    ) async throws -> (HTTPResponse, HTTPBody?) {
        var request = request
        request.headerFields[.authorization] = "Bearer \(token)"
        return try await next(request, body, baseURL)
    }
}

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
        // The format style TRUNCATES the fractional field, and millisecond
        // values are rarely exact in binary — formatting through it would
        // re-encode roughly half of all decoded server timestamps one
        // millisecond low. Split the rounded epoch milliseconds instead:
        // the whole seconds are exact, and the millisecond field is printed
        // as an integer.
        let epochMillis = (date.timeIntervalSince1970 * 1000).rounded()
        var millis = epochMillis.truncatingRemainder(dividingBy: 1000)
        if millis < 0 { millis += 1000 }
        let seconds = Date(timeIntervalSince1970: (epochMillis - millis) / 1000)
        return wholeSecond.format(seconds).dropLast() + String(format: ".%03dZ", Int(millis))
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

import Foundation
import HTTPTypes
import OpenAPIRuntime
import Testing

@testable import ATCAppServerAPI

/// Serves one canned JSON body for every request, capturing what the client sent.
private struct StubTransport: ClientTransport {
    let json: String
    let recorded: Recorder

    final class Recorder: @unchecked Sendable {
        var request: HTTPRequest?
    }

    func send(
        _ request: HTTPRequest,
        body: HTTPBody?,
        baseURL: URL,
        operationID: String
    ) async throws -> (HTTPResponse, HTTPBody?) {
        recorded.request = request
        var response = HTTPResponse(status: .ok)
        response.headerFields[.contentType] = "application/json"
        return (response, HTTPBody(json))
    }
}

private func client(returning json: String) throws -> (Client, StubTransport.Recorder) {
    let recorder = StubTransport.Recorder()
    // Servers.Server1 and ATCDateTranscoder are the documented construction
    // path (ATCAppServerAPI.swift), so building the client through them keeps
    // both the servers entry and the date coding under test.
    let client = Client(
        serverURL: try Servers.Server1.url(),
        configuration: Configuration(dateTranscoder: ATCDateTranscoder()),
        transport: StubTransport(json: json, recorded: recorder)
    )
    return (client, recorder)
}

// Type-level exercises of the generated client: operation methods, request
// paths, and response payload types all come from the OpenAPI artifact, so
// these tests fail if generation drifts from the contract.

@Suite("Generated App Server client")
struct ClientTests {
    @Test("getHealth decodes the documented payload")
    func health() async throws {
        let (client, recorder) = try client(returning: #"{"status":"ok"}"#)
        // status is a single-case literal enum, so a successful decode is the
        // real assertion.
        _ = try await client.getHealth().ok.body.json
        #expect(recorder.request?.path == "/api/v1/health")
        #expect(recorder.request?.method == .get)
    }

    @Test("getVersion decodes the documented payload")
    func version() async throws {
        let (client, recorder) = try client(
            returning: #"{"version":"0.1.0","apiVersion":"v1","commit":"dev","builtAt":"dev"}"#
        )
        let version = try await client.getVersion().ok.body.json
        #expect(version.version == "0.1.0")
        #expect(version.commit == "dev")
        #expect(version.builtAt == "dev")
        #expect(recorder.request?.path == "/api/v1/version")
        #expect(recorder.request?.method == .get)
    }

    @Test("the default server is the local loopback App Server")
    func defaultServer() throws {
        #expect(try Servers.Server1.url() == URL(string: "http://127.0.0.1:7331"))
    }

    // The bearer-auth seam remote servers will rely on: the middleware must
    // actually stamp the header (deleting it would otherwise pass silently).
    @Test("BearerAuthMiddleware stamps the Authorization header")
    func bearerMiddleware() async throws {
        let middleware = BearerAuthMiddleware(token: "token-1")
        var forwarded: HTTPRequest?
        _ = try await middleware.intercept(
            HTTPRequest(method: .get, scheme: nil, authority: nil, path: "/api/v1/health"),
            body: nil,
            baseURL: URL(string: "http://127.0.0.1:7331")!,
            operationID: "getHealth"
        ) { request, body, _ in
            forwarded = request
            return (HTTPResponse(status: .ok), body)
        }
        #expect(forwarded?.headerFields[.authorization] == "Bearer token-1")
    }

    // Regression: optional contract fields must survive generation as plain
    // strings. Earlier schema shapes made the pinned generator either drop
    // the fields entirely (nullable unions) or wrap them in single-value
    // payload structs (allOf check fragments).
    @Test("updateProject can send both PATCH fields as plain strings")
    func updateProjectFields() throws {
        let payload = Components.Schemas.UpdateProjectRequest(
            name: "Renamed",
            defaultWorkingDirectory: "/code/atc"
        )
        let encoded = try JSONEncoder().encode(payload)
        let json = try JSONSerialization.jsonObject(with: encoded) as? [String: String]
        #expect(json == ["name": "Renamed", "defaultWorkingDirectory": "/code/atc"])
    }

    @Test("fs check decodes payloads with and without a reason")
    func fsCheckReason() throws {
        // checkedAt is format: date-time, so it decodes as a Date; the
        // transcoder's lenient side accepts the whole-second fixture strings.
        let decoder = JSONDecoder()
        let transcoder = ATCDateTranscoder()
        decoder.dateDecodingStrategy = .custom { decoder in
            let container = try decoder.singleValueContainer()
            return try transcoder.decode(try container.decode(String.self))
        }
        let conclusive = try decoder.decode(
            Components.Schemas.FsCheckResponse.self,
            from: Data(
                #"{"path":"/code/atc","state":"available","checkedAt":"2026-08-02T12:00:00Z"}"#
                    .utf8
            )
        )
        #expect(conclusive.state == .available)
        #expect(conclusive.reason == nil)
        #expect(conclusive.checkedAt == Date(timeIntervalSince1970: 1_785_672_000))

        let timedOut = try decoder.decode(
            Components.Schemas.FsCheckResponse.self,
            from: Data(
                #"{"path":"/code/atc","state":"unknown","checkedAt":"2026-08-02T12:00:00Z","reason":"timeout"}"#
                    .utf8
            )
        )
        #expect(timedOut.state == .unknown)
        #expect(timedOut.reason == "timeout")
    }

    // Regression: contract timestamps are `format: date-time`, so generated
    // models carry `Date` — and the server's actual encoding (JavaScript
    // `Date.toISOString()`, always three fractional digits) must survive the
    // configured transcoder both ways. The runtime's default `.iso8601`
    // rejects fractional seconds, which is exactly why ATCDateTranscoder
    // exists.
    @Test("server timestamps round-trip the configured date transcoder")
    func timestampRoundTrip() async throws {
        let (client, _) = try client(
            returning: #"""
                {"id":"0198a001-0000-7000-8000-000000000000","name":"Atelier",
                 "defaultWorkingDirectory":"/code/atc",
                 "createdAt":"2026-08-06T12:34:56.789Z",
                 "updatedAt":"2026-08-06T12:34:56.789Z"}
                """#
        )
        let project = try await client.getProject(path: .init(projectId: "p1")).ok.body.json
        #expect(project.createdAt == Date(timeIntervalSince1970: 1_786_019_696.789))
        // Byte-identical re-encode: what the transcoder writes is what the
        // server wrote.
        #expect(try ATCDateTranscoder().encode(project.createdAt) == "2026-08-06T12:34:56.789Z")
    }

    @Test("pinThread decodes pinnedAt as a date")
    func pinThread() async throws {
        let (client, recorder) = try client(
            returning:
                #"{"id":"t1","projectId":"p1","agentId":"codex","workingDirectory":"/code/atc","settings":{"model":"gpt-5.6-sol","reasoning":"high","mode":"chat","access":"auto"},"activityState":"idle","unread":false,"pinnedAt":"2026-08-06T12:34:56.789Z","createdAt":"2026-08-06T12:00:00.000Z","updatedAt":"2026-08-06T12:34:56.789Z"}"#
        )
        let thread = try await client.pinThread(path: .init(threadId: "t1")).ok.body.json
        #expect(thread.pinnedAt == Date(timeIntervalSince1970: 1_786_019_696.789))
        #expect(recorder.request?.path == "/api/v1/threads/t1/pin")
        #expect(recorder.request?.method == .post)
    }

    // Every millisecond value must survive decode → encode byte-identically.
    // Most milliseconds are not exact in binary, and the ISO8601 format style
    // truncates — a single sample would pass or fail by luck.
    @Test("all thousand millisecond values round-trip byte-identically")
    func exhaustiveMillisecondRoundTrip() throws {
        let transcoder = ATCDateTranscoder()
        for millis in 0..<1000 {
            let wire = String(format: "2026-08-06T12:34:56.%03dZ", millis)
            let roundTripped = try transcoder.encode(try transcoder.decode(wire))
            #expect(roundTripped == wire)
        }
    }

    @Test("the transcoder decodes both fractional and whole-second timestamps")
    func lenientDecoding() throws {
        let transcoder = ATCDateTranscoder()
        #expect(
            try transcoder.decode("2026-08-06T12:34:56.789Z")
                == Date(timeIntervalSince1970: 1_786_019_696.789)
        )
        #expect(
            try transcoder.decode("2026-08-06T12:34:56Z")
                == Date(timeIntervalSince1970: 1_786_019_696)
        )
        #expect(throws: DecodingError.self) {
            _ = try transcoder.decode("yesterday")
        }
    }
}

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
    // Servers.Server1 is the documented construction path, so building the
    // client through it keeps the servers entry in the contract under test.
    let client = Client(
        serverURL: try Servers.Server1.url(),
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
        #expect(try Servers.Server1.url() == URL(string: "http://127.0.0.1:7332"))
    }
}

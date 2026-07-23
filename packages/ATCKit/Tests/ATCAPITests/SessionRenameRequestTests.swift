import Foundation
import Testing
@testable import ATCAPI

private final class SessionRenameURLProtocol: URLProtocol {
    nonisolated(unsafe) static var lastRequest: URLRequest?
    nonisolated(unsafe) static var lastBody: Data?

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        Self.lastRequest = request
        Self.lastBody = request.httpBody ?? request.httpBodyStream.flatMap(readAll)
        let body = Data(#"""
        {
          "id":"ses_123","sessionIndex":4,"name":"Renamed","actionId":"act_123456789abcdefghijklmnopq",
          "actionName":"Editor","isAgent":false,"workingDir":"/repo","status":"ended",
          "createdAt":"2026-07-18T10:00:00Z","updatedAt":"2026-07-18T11:00:00Z"
        }
        """#.utf8)
        let response = HTTPURLResponse(
            url: request.url!, statusCode: 200, httpVersion: "HTTP/1.1", headerFields: nil
        )!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: body)
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}

    private func readAll(_ stream: InputStream) -> Data {
        stream.open()
        defer { stream.close() }
        var data = Data()
        var buffer = [UInt8](repeating: 0, count: 1024)
        while stream.hasBytesAvailable {
            let count = stream.read(&buffer, maxLength: buffer.count)
            if count <= 0 { break }
            data.append(buffer, count: count)
        }
        return data
    }
}

@Suite("Session rename requests", .serialized)
struct SessionRenameRequestTests {
    private func makeClient() -> HTTPATCClient {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [SessionRenameURLProtocol.self]
        let server = ATCServer(baseURL: URL(string: "http://example.com:7331")!)
        return HTTPATCClient(server: server, session: URLSession(configuration: configuration))
    }

    @Test("renameSession PATCHes the name and decodes Session")
    func renameSession() async throws {
        let detail = try await makeClient().renameSession(id: "ses_123", name: "Renamed")
        let request = try #require(SessionRenameURLProtocol.lastRequest)
        #expect(request.httpMethod == "PATCH")
        #expect(request.url?.absoluteString == "http://example.com:7331/api/sessions/ses_123")
        let body = try #require(SessionRenameURLProtocol.lastBody)
        let json = try JSONSerialization.jsonObject(with: body) as? [String: String]
        #expect(json == ["name": "Renamed"])
        #expect(detail.id == "ses_123")
        #expect(detail.sessionIndex == 4)
        #expect(detail.name == "Renamed")
        #expect(detail.status == .ended)
    }

    @Test("renameSession encodes an explicit null when clearing")
    func clearSessionName() async throws {
        _ = try await makeClient().renameSession(id: "ses_123", name: nil)
        let body = try #require(SessionRenameURLProtocol.lastBody)
        let json = try #require(JSONSerialization.jsonObject(with: body) as? [String: Any])
        #expect(json.keys.sorted() == ["name"])
        #expect(json["name"] is NSNull)
    }
}

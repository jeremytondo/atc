import Foundation
import Testing
@testable import ATCAPI

/// Captures the request URL and serves a canned 200 body so
/// `HTTPATCClient`'s real query construction can be asserted.
private final class CapturingURLProtocol: URLProtocol {
    nonisolated(unsafe) static var lastURL: URL?

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        Self.lastURL = request.url
        let body = Data(#"{"path":"/x","truncated":false,"entries":[]}"#.utf8)
        let response = HTTPURLResponse(
            url: request.url!, statusCode: 200, httpVersion: "HTTP/1.1", headerFields: nil
        )!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: body)
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}
}

@Suite("File system request URLs", .serialized)
struct FileSystemRequestTests {
    private func makeClient() -> HTTPATCClient {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [CapturingURLProtocol.self]
        let server = ATCServer(baseURL: URL(string: "http://example.com:7331")!)
        return HTTPATCClient(server: server, session: URLSession(configuration: configuration))
    }

    @Test("fs/list sends path; showHidden omitted when false")
    func defaultQuery() async throws {
        _ = try await makeClient().listDirectory(path: "/home/j/Projects")
        #expect(CapturingURLProtocol.lastURL?.absoluteString
            == "http://example.com:7331/api/fs/list?path=/home/j/Projects")
    }

    @Test("fs/list appends showHidden only when true")
    func showHiddenQuery() async throws {
        _ = try await makeClient().listDirectory(path: "/home/j", showHidden: true)
        #expect(CapturingURLProtocol.lastURL?.absoluteString
            == "http://example.com:7331/api/fs/list?path=/home/j&showHidden=true")
    }

    @Test("fs/list percent-encodes the path")
    func percentEncoding() async throws {
        _ = try await makeClient().listDirectory(path: "/home/j/My Projects#1")
        #expect(CapturingURLProtocol.lastURL?.absoluteString
            == "http://example.com:7331/api/fs/list?path=/home/j/My%20Projects%231")
    }

    @Test("fs/list sends empty path for server default directory")
    func emptyPath() async throws {
        _ = try await makeClient().listDirectory(path: "")
        #expect(CapturingURLProtocol.lastURL?.absoluteString
            == "http://example.com:7331/api/fs/list?path=")
    }
}

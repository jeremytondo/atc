import Foundation
import Testing
@testable import CockpitAPI

/// Fixtures captured from the live server on 2026-07-06.

/// `GET /api/fs/roots`
private let rootsFixture = Data(#"""
{"roots":[{"label":"Home","path":"/home/jeremytondo"}]}
"""#.utf8)

/// `GET /api/fs/list?path=/home/jeremytondo/fixture-tree&showHidden=true` —
/// a tree built to exercise every entry shape: plain dir, dir symlink,
/// hidden file, broken symlink (`unknown`, no size/modifiedAt), file
/// symlink, plain file.
private let listFixture = Data(#"""
{"path":"/home/jeremytondo/fixture-tree","truncated":false,"entries":[{"name":"link-dir","path":"/home/jeremytondo/fixture-tree/link-dir","kind":"directory","isSymlink":true,"modifiedAt":"2026-07-06T14:21:52.175602772Z"},{"name":"subdir","path":"/home/jeremytondo/fixture-tree/subdir","kind":"directory","isSymlink":false,"modifiedAt":"2026-07-06T14:21:52.175602772Z"},{"name":".hidden","path":"/home/jeremytondo/fixture-tree/.hidden","kind":"file","isSymlink":false,"size":2,"modifiedAt":"2026-07-06T14:21:52.175602772Z"},{"name":"dangling","path":"/home/jeremytondo/fixture-tree/dangling","kind":"unknown","isSymlink":true},{"name":"link-file","path":"/home/jeremytondo/fixture-tree/link-file","kind":"file","isSymlink":true,"size":6,"modifiedAt":"2026-07-06T14:21:52.175602772Z"},{"name":"readme.md","path":"/home/jeremytondo/fixture-tree/readme.md","kind":"file","isSymlink":false,"size":6,"modifiedAt":"2026-07-06T14:21:52.175602772Z"}]}
"""#.utf8)

/// `GET /api/fs/list?path=/home/jeremytondo/fixture-tree/subdir`
private let emptyListFixture = Data(#"""
{"path":"/home/jeremytondo/fixture-tree/subdir","truncated":false,"entries":[]}
"""#.utf8)

/// Hand-trimmed from a capped-directory capture — the 10,000 verbatim
/// entries are elided; the flag is what matters.
private let truncatedListFixture = Data(#"""
{"path":"/home/jeremytondo/huge","truncated":true,"entries":[{"name":"0001","path":"/home/jeremytondo/huge/0001","kind":"file","isSymlink":false,"size":0,"modifiedAt":"2026-07-06T14:21:52.175602772Z"}]}
"""#.utf8)

@Suite("File system decoding")
struct FileSystemDecodingTests {
    @Test("roots envelope decodes")
    func rootsDecode() throws {
        let envelope = try JSONDecoder.cockpit().decode(RootsEnvelope.self, from: rootsFixture)
        #expect(envelope.roots == [RemoteWorkspaceRoot(label: "Home", path: "/home/jeremytondo")])
        #expect(envelope.roots[0].id == "/home/jeremytondo")
    }

    @Test("listing decodes with real server shapes")
    func listDecodes() throws {
        let listing = try JSONDecoder.cockpit().decode(DirectoryListing.self, from: listFixture)
        #expect(listing.path == "/home/jeremytondo/fixture-tree")
        #expect(!listing.truncated)
        #expect(listing.entries.count == 6)

        let linkDir = listing.entries[0]
        #expect(linkDir.kind == .directory)
        #expect(linkDir.isSymlink)
        #expect(linkDir.size == nil)
        #expect(linkDir.modifiedAt != nil)

        let hidden = listing.entries[2]
        #expect(hidden.name == ".hidden")
        #expect(hidden.kind == .file)
        #expect(hidden.size == 2)

        let dangling = listing.entries[3]
        #expect(dangling.kind == .unknown)
        #expect(dangling.isSymlink)
        #expect(dangling.size == nil)
        #expect(dangling.modifiedAt == nil)

        let file = listing.entries[5]
        #expect(file.id == file.path)
        #expect(file.kind == .file)
        #expect(!file.isSymlink)
        #expect(file.size == 6)
    }

    @Test("empty directory decodes to empty entries")
    func emptyDecodes() throws {
        let listing = try JSONDecoder.cockpit().decode(DirectoryListing.self, from: emptyListFixture)
        #expect(listing.entries.isEmpty)
        #expect(!listing.truncated)
    }

    @Test("truncated flag decodes")
    func truncatedDecodes() throws {
        let listing = try JSONDecoder.cockpit().decode(DirectoryListing.self, from: truncatedListFixture)
        #expect(listing.truncated)
    }

    @Test("unrecognized kind decodes as unknown")
    func forwardCompatibleKind() throws {
        let body = Data(#"""
        {"path":"/x","truncated":false,"entries":[{"name":"pipe","path":"/x/pipe","kind":"named-pipe","isSymlink":false}]}
        """#.utf8)
        let listing = try JSONDecoder.cockpit().decode(DirectoryListing.self, from: body)
        #expect(listing.entries[0].kind == .unknown)
    }
}

/// Error bodies captured from the live server on 2026-07-06
/// (`internal_error` synthesized — not triggerable on demand).
@Suite("File system error envelopes")
struct FileSystemErrorTests {
    private static let bodies: [(code: String, json: String)] = [
        ("invalid_path", #"{"error":"invalid_path","message":"invalid path: path must be absolute"}"#),
        ("outside_browsable_roots", #"{"error":"outside_browsable_roots","message":"outside browsable roots: /etc/passwd"}"#),
        ("not_found", #"{"error":"not_found","message":"not found: /home/jeremytondo/nope-not-here"}"#),
        ("not_directory", #"{"error":"not_directory","message":"not a directory: /home/jeremytondo/fixture-tree/readme.md"}"#),
        ("permission_denied", #"{"error":"permission_denied","message":"permission denied: /home/jeremytondo/fixture-tree/locked"}"#),
        ("internal_error", #"{"error":"internal_error","message":"internal error"}"#),
    ]

    @Test("each FS error body decodes and surfaces its code", arguments: bodies.map(\.code))
    func decodes(code: String) throws {
        let body = Data(Self.bodies.first { $0.code == code }!.json.utf8)
        let envelope = try JSONDecoder.cockpit().decode(ErrorEnvelope.self, from: body)
        #expect(envelope.error == code)
        let error = CockpitError.api(code: envelope.error, message: envelope.message, sessionID: envelope.sessionId)
        #expect(error.apiCode == code)
        #expect(error.errorDescription == envelope.message)
    }
}

import Foundation
import Testing
import ATCAppServerAPI
@testable import ATC

/// DirectoryPickerModel: server-backed navigation and the keep-last-listing
/// failure behavior the picker relies on.
@MainActor
@Suite("DirectoryPickerModel")
struct DirectoryPickerModelTests {
    private static func listing(
        path: String, parent: String?, names: [String]
    ) -> Components.Schemas.FsListResponse {
        .init(path: path, parent: parent, entries: names.map { .init(name: $0, path: "\(path)/\($0)") })
    }

    @Test("lists home for a nil path, descends into an entry, and climbs back up")
    func navigation() async {
        let client = ScriptableAppServerClient()
        client.directoryListings = [
            nil: Self.listing(path: "/home/dev", parent: "/home", names: ["Projects"]),
            "/home/dev": Self.listing(path: "/home/dev", parent: "/home", names: ["Projects"]),
            "/home/dev/Projects": Self.listing(
                path: "/home/dev/Projects", parent: "/home/dev", names: []),
        ]
        let model = DirectoryPickerModel(client: client)

        await model.load(path: nil)
        #expect(model.currentPath == "/home/dev")
        #expect(model.canGoUp)
        #expect(model.listing?.entries.map(\.name) == ["Projects"])

        await model.descend(into: model.listing!.entries[0])
        #expect(model.currentPath == "/home/dev/Projects")
        #expect(model.listing?.entries.isEmpty == true)

        await model.goUp()
        #expect(model.currentPath == "/home/dev")
    }

    @Test("a 422 surfaces the server's diagnostic and keeps the last good listing")
    func failureKeepsListing() async {
        let client = ScriptableAppServerClient()
        client.directoryListings = [
            nil: Self.listing(path: "/home/dev", parent: "/home", names: ["gone"])
        ]
        let model = DirectoryPickerModel(client: client)
        await model.load(path: nil)

        client.directoryListingFailure = .init(
            _tag: .directoryUnavailable,
            path: "/home/dev/gone",
            state: .missing,
            message: "/home/dev/gone does not exist"
        )
        await model.load(path: "/home/dev/gone")
        #expect(model.currentPath == "/home/dev")
        #expect(model.errorMessage == "/home/dev/gone does not exist")

        client.directoryListingFailure = nil
        await model.load(path: nil)
        #expect(model.errorMessage == nil)
    }

    @Test("start falls back to the server home when the initial path won't list")
    func startFallsBackToHome() async {
        let client = ScriptableAppServerClient()
        client.directoryListings = [
            nil: Self.listing(path: "/home/dev", parent: "/home", names: [])
        ]
        let model = DirectoryPickerModel(client: client)

        await model.start(at: "/stale/typed/path")
        #expect(model.currentPath == "/home/dev")
        #expect(model.errorMessage == nil)
    }
}

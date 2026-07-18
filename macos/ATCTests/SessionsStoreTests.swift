import Testing
import ATCAPI
@testable import ATC

@Suite("SessionsStore")
struct SessionsStoreTests {
    @Test("rename merges the authoritative Session detail immediately")
    func renameMerges() async throws {
        let store = SessionsStore(client: StatefulWorkspacesClient())
        await store.refresh()
        let target = try #require(store.sessions.first { $0.id == "ses_running" })

        let renamed = try await store.rename(id: target.id, name: "  Parser review  ")

        #expect(renamed.name == "Parser review")
        #expect(store.session(id: target.id)?.name == "Parser review")
        #expect(store.session(id: target.id)?.id == target.id)
        #expect(store.session(id: target.id)?.status == target.status)
    }
}

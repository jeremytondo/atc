import Testing
import ATCAPI
@testable import ATC

@Suite("SessionsStore")
struct SessionsStoreTests {
    @Test("start returns the Action identity copied onto the Session")
    func startCopiesActionIdentity() async throws {
        let store = SessionsStore(client: MockATCClient())

        let started = try await store.start(StartSessionRequest(
            workspaceId: "wsp_parser",
            actionId: "act_vpj2tlg9viqd8ms52ptuvao5c4"
        ))

        #expect(started.actionId == "act_vpj2tlg9viqd8ms52ptuvao5c4")
        #expect(started.actionName == "Claude")
        #expect(started.isAgent)
    }

    @Test("start without an Action ID returns an Interactive Shell Session")
    func startInteractiveShell() async throws {
        let store = SessionsStore(client: MockATCClient())

        let started = try await store.start(StartSessionRequest(
            workspaceId: "wsp_parser"
        ))

        #expect(started.actionId == nil)
        #expect(started.actionName == nil)
        #expect(!started.isAgent)
    }

    @Test("rename merges the authoritative Session immediately")
    func renameMerges() async throws {
        let store = SessionsStore(client: StatefulWorkspacesClient())
        await store.refresh()
        let target = try #require(store.sessions.first { $0.id == "ses_running" })

        let renamed = try await store.rename(id: target.id, name: "  Parser review  ")

        #expect(renamed.name == "Parser review")
        #expect(store.session(id: target.id)?.name == "Parser review")
        #expect(store.session(id: target.id)?.id == target.id)
        #expect(store.session(id: target.id)?.status == target.status)
        #expect(renamed.actionId == target.actionId)
        #expect(renamed.actionName == target.actionName)
        #expect(renamed.isAgent == target.isAgent)
    }
}

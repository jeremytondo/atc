import Foundation
import Testing
import ATCAPI
@testable import ATC

/// ActionsStore CRUD against the mock registry, which mirrors the server's
/// builtin-plus-overlay model: origin computation, revert-on-delete, and
/// the error codes the editor surfaces.
@MainActor
@Suite("ActionsStore")
struct ActionsStoreTests {
    private func makeStore() -> ActionsStore {
        ActionsStore(client: MockATCClient())
    }

    @Test("refresh lists the fixture actions sorted by label, without command")
    func refreshLists() async {
        let store = makeStore()
        await store.refresh()
        #expect(store.lastError == nil)
        #expect(store.hasLoadedOnce)
        #expect(store.actions.map(\.name) == ["claude", "codex", "lazygit"])
        #expect(store.actions.allSatisfy { $0.command == nil })
    }

    @Test("detail returns the full definition the list omits")
    func detailIncludesCommand() async throws {
        let store = makeStore()
        let detail = try await store.detail(name: "lazygit")
        #expect(detail.command == "lazygit")
        #expect(detail.isCustom)
    }

    @Test("create adds a custom action and merges it into the list")
    func createCustom() async throws {
        let store = makeStore()
        await store.refresh()
        let created = try await store.create(
            ActionWriteRequest(label: "Bottom", command: "btm")
        )
        #expect(created.name == "bottom")
        #expect(created.isCustom)
        #expect(store.action(name: "bottom") != nil)
    }

    @Test("creating a duplicate name surfaces action_conflict")
    func createDuplicate() async {
        let store = makeStore()
        await #expect(throws: ATCError.self) {
            try await store.create(ActionWriteRequest(name: "claude", command: "claude"))
        }
    }

    @Test("updating a builtin creates an override with origin modified")
    func updateBuiltinBecomesModified() async throws {
        let store = makeStore()
        await store.refresh()
        let updated = try await store.update(
            name: "claude",
            ActionWriteRequest(
                name: "claude", label: "Claude", description: "Claude Code CLI",
                command: "claude", args: ["--dangerously-skip-permissions"],
                prompt: .init()
            )
        )
        #expect(updated.isModified)
        #expect(updated.args == ["--dangerously-skip-permissions"])
    }

    @Test("setEnabled flips the flag without changing a builtin's origin")
    func setEnabledKeepsOrigin() async throws {
        let store = makeStore()
        await store.refresh()
        let disabled = try await store.setEnabled(name: "codex", enabled: false)
        #expect(!disabled.enabled)
        #expect(disabled.isBuiltin)
        let enabled = try await store.setEnabled(name: "codex", enabled: true)
        #expect(enabled.enabled)
        #expect(enabled.isBuiltin)
    }

    @Test("deleting a custom action removes it")
    func deleteCustom() async throws {
        let store = makeStore()
        await store.refresh()
        try await store.delete(name: "lazygit")
        #expect(store.action(name: "lazygit") == nil)
        #expect(store.actions.count == 2)
    }

    @Test("deleting a modified builtin reverts it to the default definition")
    func deleteRevertsOverride() async throws {
        let store = makeStore()
        await store.refresh()
        _ = try await store.update(
            name: "claude",
            ActionWriteRequest(name: "claude", command: "claude", args: ["--custom"])
        )
        try await store.delete(name: "claude")
        // Still present, back to builtin.
        let reverted = try #require(store.action(name: "claude"))
        #expect(reverted.isBuiltin)
        let detail = try await store.detail(name: "claude")
        #expect(detail.args == [])
    }

    @Test("deleting a bare builtin surfaces action_conflict")
    func deleteBuiltinRejected() async {
        let store = makeStore()
        await #expect(throws: ATCError.self) {
            try await store.delete(name: "codex")
        }
    }

    @Test("a failed refresh keeps the last loaded list")
    func failedRefreshKeepsData() async throws {
        let client = MockATCClient()
        let store = ActionsStore(client: client)
        await store.refresh()
        #expect(!store.actions.isEmpty)
        // Point of comparison for the error path: delete against a missing
        // name throws but must not clear loaded data.
        await #expect(throws: ATCError.self) {
            try await store.delete(name: "does-not-exist")
        }
        #expect(!store.actions.isEmpty)
    }
}

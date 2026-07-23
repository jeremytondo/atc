import Testing
import ATCAPI
@testable import ATC

@MainActor
@Suite("ActionsStore")
struct ActionsStoreTests {
    private func makeStore() -> ActionsStore {
        ActionsStore(client: MockATCClient())
    }

    @Test("refresh lists complete actions sorted by name")
    func refreshLists() async {
        let store = makeStore()
        await store.refresh()

        #expect(store.lastError == nil)
        #expect(store.hasLoadedOnce)
        #expect(store.actions.map(\.name) == ["Claude", "Codex", "LazyGit"])
        #expect(store.actions.allSatisfy { !$0.command.isEmpty })
    }

    @Test("create merges a complete action keyed by generated ID")
    func create() async throws {
        let store = makeStore()
        await store.refresh()

        let created = try await store.create(
            ActionCreate(
                name: "Bottom",
                description: "System monitor",
                command: "btm",
                args: ["--basic"],
                isAgent: false
            )
        )

        #expect(created.id.hasPrefix("act_"))
        #expect(store.action(id: created.id) == created)
    }

    @Test("duplicate names are allowed and IDs remain distinct")
    func duplicateNames() async throws {
        let store = makeStore()
        await store.refresh()

        let first = try await store.create(ActionCreate(name: "Tool", command: "one"))
        let second = try await store.create(ActionCreate(name: "Tool", command: "two"))

        #expect(first.id != second.id)
        #expect(store.actions.filter { $0.name == "Tool" }.count == 2)
    }

    @Test("patch can rename, clear description, reclassify, and disable")
    func update() async throws {
        let store = makeStore()
        await store.refresh()
        let target = try #require(store.actions.first { $0.name == "Claude" })

        let updated = try await store.update(
            id: target.id,
            ActionPatch(
                name: "Claude Code",
                clearDescription: true,
                args: ["--verbose"],
                enabled: false,
                isAgent: false
            )
        )

        #expect(updated.id == target.id)
        #expect(updated.name == "Claude Code")
        #expect(updated.description == nil)
        #expect(updated.args == ["--verbose"])
        #expect(!updated.enabled)
        #expect(!updated.isAgent)
    }

    @Test("enable and disable are patches")
    func setEnabled() async throws {
        let store = makeStore()
        await store.refresh()
        let target = try #require(store.actions.first { $0.name == "Codex" })

        let disabled = try await store.setEnabled(id: target.id, enabled: false)
        let enabled = try await store.setEnabled(id: target.id, enabled: true)

        #expect(!disabled.enabled)
        #expect(enabled.enabled)
    }

    @Test("delete hard-removes any action")
    func delete() async throws {
        let store = makeStore()
        await store.refresh()
        let target = try #require(store.actions.first { $0.name == "Codex" })

        try await store.delete(id: target.id)

        #expect(store.action(id: target.id) == nil)
        #expect(store.actions.count == 2)
    }

    @Test("a failed mutation keeps the loaded list")
    func failedMutationKeepsData() async {
        let store = makeStore()
        await store.refresh()

        await #expect(throws: ATCError.self) {
            try await store.delete(id: "act_missing")
        }

        #expect(!store.actions.isEmpty)
    }
}

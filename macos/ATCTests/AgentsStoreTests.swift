import ATCAppServerAPI
import Foundation
import Testing

@testable import ATC

/// AgentsStore's on-demand reads: every ask re-reads (the server owns
/// freshness), the held value stays up until the read lands, and a failed
/// re-read keeps it.
@MainActor
@Suite("AgentsStore")
struct AgentsStoreTests {
    private typealias AgentCommand = Components.Schemas.AgentCommand

    @Test("every command ask re-reads, so a newly added skill shows on the next ask")
    func commandsRevalidateOnEachAsk() async {
        let client = ScriptableAppServerClient()
        client.agentCommands = [.claudeCode: [AgentCommand(name: "commit", description: "Commit")]]
        let store = AgentsStore(client: client)

        store.loadCommands(for: .claudeCode, dir: "/home/dev/app")
        await settle { store.commands(for: .claudeCode, dir: "/home/dev/app") != nil }
        #expect(store.commands(for: .claudeCode, dir: "/home/dev/app")?.map(\.name) == ["commit"])

        client.agentCommands = [
            .claudeCode: [
                AgentCommand(name: "commit", description: "Commit"),
                AgentCommand(name: "review", description: "Review"),
            ]
        ]
        store.loadCommands(for: .claudeCode, dir: "/home/dev/app")
        await settle { store.commands(for: .claudeCode, dir: "/home/dev/app")?.count == 2 }
        #expect(client.commandListings.count == 2)
    }

    @Test("a failed re-read keeps the held command list")
    func failedCommandRereadKeepsHeldList() async {
        let client = ScriptableAppServerClient()
        client.agentCommands = [.claudeCode: [AgentCommand(name: "commit", description: "Commit")]]
        let store = AgentsStore(client: client)

        store.loadCommands(for: .claudeCode, dir: "/home/dev/app")
        await settle { store.commands(for: .claudeCode, dir: "/home/dev/app") != nil }

        client.shouldFail = true
        store.loadCommands(for: .claudeCode, dir: "/home/dev/app")
        // A failed read leaves no trace on the client, so give it a bounded
        // moment, then prove the store still serves the held list and the
        // next successful ask goes through (the failure cleared the in-flight
        // guard).
        await settle(until: { false }, timeout: .milliseconds(50))
        #expect(store.commands(for: .claudeCode, dir: "/home/dev/app")?.map(\.name) == ["commit"])

        client.shouldFail = false
        client.agentCommands = [.claudeCode: []]
        store.loadCommands(for: .claudeCode, dir: "/home/dev/app")
        await settle { store.commands(for: .claudeCode, dir: "/home/dev/app")?.isEmpty == true }
        #expect(client.commandListings.count == 2)
    }

    @Test("a failed catalog re-read keeps the held catalog")
    func failedModelRereadKeepsHeldCatalog() async {
        let client = ScriptableAppServerClient()
        client.models = [.codex: [Fixtures.model("gpt-5", displayName: "GPT-5")]]
        let store = AgentsStore(client: client)

        store.loadModels(for: .codex)
        await settle { store.models(for: .codex) != nil }

        client.shouldFail = true
        store.loadModels(for: .codex)
        await settle { store.modelErrors[.codex] != nil }
        #expect(store.models(for: .codex)?.map(\.value) == ["gpt-5"])
    }
}

// Shared domain state for one Connection's agent registry: live-detected
// availability plus the actionable reason when a provider is missing. The
// server never persists this — every refresh is a fresh detection — so the
// store refreshes with the SSE-driven cycle like every other list.
//
// The per-agent model catalogs (ATC-205) and per-agent, per-directory
// command lists (ATC-216) are read on demand and held only for rendering:
// the server owns their freshness (it caches each for a short while and
// re-asks the provider lazily, on the next request after expiry), so every
// ask here re-reads — serving what is held immediately and replacing it
// when the read lands (stale-while-revalidate). A catalog is asked for when
// a Chat of the agent appears; a command list whenever a composer's `/`
// needs suggestions. Neither polls. A failed re-read keeps what is held; a
// failed first read of a catalog records its error for the picker, while an
// absent command list is never an error — it simply means no `/`
// suggestions. Nothing is kept across a URL/token rebuild of the runtime;
// an SSE reconnect keeps the store, so freshness comes from re-asking, not
// from reconnecting.

import ATCAppServerAPI
import Foundation
import Observation

@Observable
final class AgentsStore {
    let client: any APIProtocol

    private(set) var agents: [Agent] = []
    /// Model catalogs by agent, once read (see the header).
    private(set) var models: [AgentID: [AgentModel]] = [:]
    /// The last failed catalog read per agent, until a read succeeds.
    private(set) var modelErrors: [AgentID: String] = [:]
    /// Command lists by agent and directory, once read (see the header).
    private(set) var commands: [AgentDirectory: [AgentCommand]] = [:]
    private let refreshState = RefreshState(category: "agents")
    private var modelReads: [AgentID: Task<Void, Never>] = [:]
    private var commandReads: [AgentDirectory: Task<Void, Never>] = [:]

    /// The key a command list is read for: one agent in one directory.
    struct AgentDirectory: Hashable {
        let agent: AgentID
        let dir: String
    }

    var hasLoadedOnce: Bool { refreshState.hasLoadedOnce }
    var lastError: String? { refreshState.lastError }
    var isResolved: Bool { refreshState.isResolved }

    init(client: any APIProtocol) {
        self.client = client
    }

    func refresh() async {
        await refreshState.run {
            try await client.listAgents().ok.body.json
        } apply: { fetched in
            agents = fetched
        }
    }

    func agent(id: AgentID) -> Agent? {
        agents.first { $0.id == id }
    }

    /// The agent's catalog, or nil until `loadModels` has read it.
    func models(for id: AgentID) -> [AgentModel]? {
        models[id]
    }

    /// The agent's commands in `dir`, or nil until `loadCommands` has read them.
    func commands(for id: AgentID, dir: String) -> [AgentCommand]? {
        commands[AgentDirectory(agent: id, dir: dir)]
    }

    /// Re-read the agent's command list for `dir` (coalesced while a read
    /// is in flight). The held list stays up until the read lands; a failed
    /// read keeps it.
    func loadCommands(for id: AgentID, dir: String) {
        let key = AgentDirectory(agent: id, dir: dir)
        guard commandReads[key] == nil else { return }
        commandReads[key] = Task { [weak self] in
            defer { self?.commandReads[key] = nil }
            guard let self else { return }
            if let fetched = try? await client.listAgentCommands(
                path: .init(agentId: id.rawValue), query: .init(dir: dir)
            ).ok.body.json {
                commands[key] = fetched
            }
        }
    }

    /// Re-read the agent's catalog (coalesced while a read is in flight).
    /// The held catalog stays up until the read lands; a failed read keeps
    /// it and records the error, which the picker shows only while no
    /// catalog is held.
    func loadModels(for id: AgentID) {
        guard modelReads[id] == nil else { return }
        modelReads[id] = Task { [weak self] in
            defer { self?.modelReads[id] = nil }
            guard let self else { return }
            do {
                models[id] = try await client.listAgentModels(path: .init(agentId: id.rawValue)).ok.body.json
                modelErrors[id] = nil
            } catch {
                modelErrors[id] = error.localizedDescription
            }
        }
    }
}

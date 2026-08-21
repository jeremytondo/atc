// Shared domain state for one Connection's agent registry: live-detected
// availability plus the actionable reason when a provider is missing. The
// server never persists this — every refresh is a fresh detection — so the
// store refreshes with the SSE-driven cycle like every other list. The
// per-agent model catalogs (ATC-205) are read on demand — the first Chat of
// an agent asks for its catalog — and kept for the store's life; the server
// caches them too, so a re-read is cheap when a view wants one. The
// per-agent, per-directory command lists (ATC-216) follow the same rule:
// read when a composer first asks, kept for the store's life, a failed read
// retried on the next ask — and never shown as an error, since an absent
// list simply means no `/` suggestions.

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

    /// Read the agent's command list for `dir` once (idempotent while a
    /// read is in flight or already landed); a failed read is retried on
    /// the next call.
    func loadCommands(for id: AgentID, dir: String) {
        let key = AgentDirectory(agent: id, dir: dir)
        guard commands[key] == nil, commandReads[key] == nil else { return }
        commandReads[key] = Task { [weak self] in
            defer { self?.commandReads[key] = nil }
            guard let self else { return }
            commands[key] = try? await client.listAgentCommands(
                path: .init(agentId: id.rawValue), query: .init(dir: dir)
            ).ok.body.json
        }
    }

    /// Read the agent's catalog once (idempotent while a read is in flight
    /// or already landed); a failed read records its error and is retried
    /// on the next call.
    func loadModels(for id: AgentID) {
        guard models[id] == nil, modelReads[id] == nil else { return }
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

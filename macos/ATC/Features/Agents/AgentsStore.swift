// Shared domain state for one Connection's agent registry: live-detected
// availability plus the actionable reason when a provider is missing. The
// server never persists this — every refresh is a fresh detection — so the
// store refreshes with the SSE-driven cycle like every other list. The
// per-agent model catalogs (ATC-205) are read on demand — the first Chat of
// an agent asks for its catalog — and kept for the store's life; the server
// caches them too, so a re-read is cheap when a view wants one.

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
    private let refreshState = RefreshState(category: "agents")
    private var modelReads: [AgentID: Task<Void, Never>] = [:]

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

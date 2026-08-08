// Shared domain state for one Connection's agent registry: live-detected
// availability plus the actionable reason when a provider is missing. The
// server never persists this — every refresh is a fresh detection — so the
// store refreshes with the SSE-driven cycle like every other list.

import ATCAppServerAPI
import Foundation
import Observation

@Observable
final class AgentsStore {
    let client: any APIProtocol

    private(set) var agents: [Agent] = []
    private let refreshState = RefreshState(category: "agents")

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
}

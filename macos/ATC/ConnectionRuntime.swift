// Everything the app runs for one configured Connection: one contract
// client, four stores, and the SSE event loop that keeps them current.
// AppModel builds and tears these down as the Connection list changes.
//
// Liveness model (no polling): the resource-change stream drives refreshes.
// Every `.connected` refetches all lists — the server guarantees the
// subscriber registered before the opening comment, so that resync is
// race-free. `.change` events are thin invalidations, coalesced ~100 ms and
// refetched per named collection (thread and terminal listings are priced —
// they consult the multiplexer). Reachability derives from the stream plus
// refresh outcomes; an unreachable Connection keeps its last-loaded data.

import ATCAppServerAPI
import ATCAppServerTransport
import Foundation
import Observation

/// Composite identity for a thread: local Connection ID plus the server's
/// record ID. Selection, the terminal registry, and every cross-Connection
/// reference use these — never a bare server ID.
struct ThreadRef: Hashable, Sendable {
    let connectionID: UUID
    let threadID: String
}

/// Composite identity for a terminal (see `ThreadRef`).
struct TerminalRef: Hashable, Sendable {
    let connectionID: UUID
    let terminalID: String
}

/// Composite identity for a project (see `ThreadRef`). Identifiable so a
/// ProjectRef can drive item-based sheet presentation directly.
struct ProjectRef: Hashable, Sendable, Identifiable {
    let connectionID: UUID
    let projectID: String

    var id: Self { self }
}

@Observable
final class ConnectionRuntime: Identifiable {
    typealias EventStreamFactory = (URL, [String: String]) -> AsyncStream<ResourceEventStream.Event>

    /// The record this runtime was built from. Name-only edits update it in
    /// place; URL/token edits rebuild the whole runtime instead.
    private(set) var record: ConnectionRecord
    let client: any APIProtocol
    /// Base URL and headers for the non-contract transports (attach
    /// WebSocket, SSE) — same origin and auth seam as the client.
    let baseURL: URL
    let transportHeaders: [String: String]
    let projects: ProjectsStore
    let threads: ThreadsStore
    let terminals: TerminalsStore
    let agents: AgentsStore

    /// Connection health for status surfaces: gray until the stream first
    /// settles, then green while the stream is open and refreshes succeed.
    /// An unreachable Connection keeps its last-loaded data.
    private(set) var reachability: Reachability = .unknown

    private let eventStreamFactory: (URL, [String: String]) -> AsyncStream<ResourceEventStream.Event>
    private var eventTask: Task<Void, Never>?
    /// Whether the SSE stream is currently open. `.connected` requires it:
    /// without the stream no invalidations flow, so a green dot would lie
    /// even while plain HTTP requests succeed.
    private var streamOpen = false
    /// Coalescing buffer: resources named by events since the last refetch.
    private var pendingResources: Set<Components.Schemas.ResourceChangedEvent.ResourcePayload> = []
    private var coalesceTask: Task<Void, Never>?

    var id: UUID { record.id }

    init(
        record: ConnectionRecord,
        client: any APIProtocol,
        baseURL: URL,
        eventStreamFactory: @escaping (URL, [String: String]) -> AsyncStream<ResourceEventStream.Event> = {
            ResourceEventStream.live(baseURL: $0, headers: $1)
        }
    ) {
        self.record = record
        self.client = client
        self.baseURL = baseURL
        transportHeaders = record.token.isEmpty
            ? [:]
            : ["Authorization": "Bearer \(record.token)"]
        self.eventStreamFactory = eventStreamFactory
        projects = ProjectsStore(client: client)
        threads = ThreadsStore(client: client)
        terminals = TerminalsStore(client: client)
        agents = AgentsStore(client: client)
    }

    /// Name-only edits don't disturb the client, stores, or attaches.
    func updateRecord(_ newRecord: ConnectionRecord) {
        record = newRecord
    }

    // MARK: - Event loop

    func start() {
        guard eventTask == nil else { return }
        let stream = eventStreamFactory(baseURL, transportHeaders)
        eventTask = Task { [weak self] in
            for await event in stream {
                guard let self, !Task.isCancelled else { return }
                switch event {
                case .connected:
                    self.streamOpen = true
                    await self.refresh()
                case .change(let change):
                    self.scheduleRefetch(of: change.resource)
                case .disconnected:
                    self.streamOpen = false
                    self.reachability = .unreachable
                }
            }
        }
    }

    func stop() {
        eventTask?.cancel()
        eventTask = nil
        coalesceTask?.cancel()
        coalesceTask = nil
        pendingResources = []
        streamOpen = false
    }

    /// One combined refresh of all four stores; the Connection is reachable
    /// iff the whole refresh succeeded. The requests interleave on I/O.
    func refresh() async {
        async let projectsDone: Void = projects.refresh()
        async let threadsDone: Void = threads.refresh()
        async let terminalsDone: Void = terminals.refresh()
        async let agentsDone: Void = agents.refresh()
        _ = await (projectsDone, threadsDone, terminalsDone, agentsDone)
        settleReachability()
    }

    /// Events arrive in bursts; collect the named collections and refetch
    /// each once per ~100 ms window instead of once per event.
    private func scheduleRefetch(of resource: Components.Schemas.ResourceChangedEvent.ResourcePayload) {
        pendingResources.insert(resource)
        startCoalesceIfNeeded()
    }

    private func startCoalesceIfNeeded() {
        guard coalesceTask == nil, !pendingResources.isEmpty else { return }
        coalesceTask = Task { [weak self] in
            try? await Task.sleep(for: .milliseconds(100))
            guard let self, !Task.isCancelled else { return }
            let resources = self.pendingResources
            self.pendingResources = []
            await withTaskGroup { group in
                if resources.contains(.project) {
                    group.addTask { await self.projects.refresh() }
                }
                if resources.contains(.thread) {
                    group.addTask { await self.threads.refresh() }
                }
                if resources.contains(.terminal) {
                    group.addTask { await self.terminals.refresh() }
                }
            }
            // A stop() during the refetch must not resurrect reachability
            // (or spawn another window) on a torn-down runtime.
            guard !Task.isCancelled else { return }
            self.settleReachability()
            self.coalesceTask = nil
            // Events that arrived while refetching wait for their own window.
            self.startCoalesceIfNeeded()
        }
    }

    private func settleReachability() {
        reachability = streamOpen && allRefreshesClean() ? .connected : .unreachable
    }

    /// Agent availability has no invalidation event (it is detected, not
    /// stored); flows that present agent choices refresh on demand.
    private func allRefreshesClean() -> Bool {
        projects.lastError == nil && threads.lastError == nil
            && terminals.lastError == nil && agents.lastError == nil
    }
}

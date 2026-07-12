import Foundation
import Observation
import ATCAPI

/// Composite identity for a session: local Connection ID plus the server's
/// record ID. Selection, the terminal registry, and every cross-Connection
/// reference use these — never a bare server ID.
struct SessionRef: Hashable {
    let connectionID: UUID
    let sessionID: String
}

/// Composite identity for a project (see `SessionRef`).
struct ProjectRef: Hashable {
    let connectionID: UUID
    let projectID: String
}

/// Composite identity for a workspace (see `SessionRef`).
struct WorkspaceRef: Hashable {
    let connectionID: UUID
    let workspaceID: String
}

/// Everything the app runs for one configured Connection: one client and one
/// pair of stores, polling on the existing cadence. `AppModel` builds and
/// tears these down as the Connection list changes; the stores themselves
/// stay single-client and unchanged.
@Observable
final class ConnectionRuntime: Identifiable {
    /// The record this runtime was built from. Name-only edits update it in
    /// place; URL/token edits rebuild the whole runtime instead.
    private(set) var record: ConnectionRecord
    let client: any ATCClient
    let projects: ProjectsStore
    let sessions: SessionsStore
    let workspaces: WorkspacesStore
    /// Part of the poll cycle so session classification (agent vs terminal)
    /// and the creation sheets never depend on stale on-demand data.
    let actions: ActionsStore

    /// Outcome of the most recent combined refresh: gray until one
    /// completes, then green/red. A red Connection keeps its last loaded
    /// data — the stores don't clear on error.
    private(set) var reachability: Reachability = .unknown

    private var pollTask: Task<Void, Never>?

    var id: UUID { record.id }

    init(record: ConnectionRecord, client: any ATCClient) {
        self.record = record
        self.client = client
        self.projects = ProjectsStore(client: client)
        self.sessions = SessionsStore(client: client)
        self.workspaces = WorkspacesStore(client: client)
        self.actions = ActionsStore(client: client)
    }

    /// Name-only edits don't disturb the client, stores, or attaches.
    func updateRecord(_ newRecord: ConnectionRecord) {
        record = newRecord
    }

    /// ~7s poll task; owned by the runtime so a deleted Connection stops
    /// polling immediately and deterministically.
    func startPolling() {
        guard pollTask == nil else { return }
        pollTask = Task { [weak self] in
            while !Task.isCancelled {
                guard let self else { return }
                await self.refresh()
                try? await Task.sleep(for: .seconds(7))
            }
        }
    }

    func stopPolling() {
        pollTask?.cancel()
        pollTask = nil
    }

    /// One combined refresh of all four stores; the Connection is
    /// reachable iff the whole refresh succeeded. The requests interleave
    /// on I/O.
    func refresh() async {
        async let projectsDone: Void = projects.refresh()
        async let sessionsDone: Void = sessions.refresh()
        async let workspacesDone: Void = workspaces.refresh()
        async let actionsDone: Void = actions.refresh()
        _ = await (projectsDone, sessionsDone, workspacesDone, actionsDone)
        reachability = (projects.lastError == nil && sessions.lastError == nil
            && workspaces.lastError == nil && actions.lastError == nil)
            ? .connected
            : .unreachable
    }
}

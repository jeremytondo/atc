import Foundation
import Observation
import OSLog
import ATCAPI

private let logger = Logger(subsystem: "ElevenIdeas.atc", category: "workspaces")

/// Shared domain state for one Connection's workspace list. Same polling
/// story as `ProjectsStore` — `ConnectionRuntime` owns the poll task; this
/// store only refreshes on demand. Always fetches archived workspaces too;
/// surfaces filter locally (the Dashboard's Show Archived toggle).
@Observable
final class WorkspacesStore {
    var client: any ATCClient {
        didSet {
            workspaces = []
            lastError = nil
            scheduleRefresh()
        }
    }

    /// Server order: newest-created first, spanning every project.
    private(set) var workspaces: [Workspace] = []
    private(set) var isLoading = false
    private(set) var hasLoadedOnce = false
    var lastError: String?

    /// Monotonic token: a slow in-flight refresh must not clobber the
    /// result of a newer one.
    private var refreshGeneration = 0

    /// The one owned follow-up refresh (post-mutation); superseded
    /// follow-ups are cancelled instead of piling up.
    private var followUpRefresh: Task<Void, Never>?

    init(client: any ATCClient) {
        self.client = client
    }

    func refresh() async {
        refreshGeneration += 1
        let generation = refreshGeneration
        isLoading = true
        defer {
            // Only the newest request settles visible state — an older
            // request finishing late must not clear the spinner for a
            // refresh that is still in flight.
            if generation == refreshGeneration {
                isLoading = false
                hasLoadedOnce = true
            }
        }
        do {
            let fetched = try await client.workspaces(projectID: nil, includeArchived: true)
            guard generation == refreshGeneration else { return }
            workspaces = fetched
            lastError = nil
            logger.debug("refreshed \(self.workspaces.count) workspaces")
        } catch {
            guard generation == refreshGeneration else { return }
            lastError = error.localizedDescription
            logger.error("refresh failed: \(error)")
        }
    }

    private func scheduleRefresh() {
        followUpRefresh?.cancel()
        followUpRefresh = Task { await refresh() }
    }

    // MARK: - Actions

    /// Creates a workspace; throws so the create sheet can show inline
    /// errors (e.g. the server's `project_archived` 409).
    @discardableResult
    func create(projectID: String, name: String) async throws -> Workspace {
        let workspace = try await client.createWorkspace(projectID: projectID, name: name)
        merge(workspace)
        scheduleRefresh()
        return workspace
    }

    @discardableResult
    func rename(id: String, name: String) async throws -> Workspace {
        let workspace = try await client.renameWorkspace(id: id, name: name)
        merge(workspace)
        scheduleRefresh()
        return workspace
    }

    @discardableResult
    func archive(id: String) async throws -> Workspace {
        let workspace = try await client.archiveWorkspace(id: id)
        merge(workspace)
        scheduleRefresh()
        return workspace
    }

    @discardableResult
    func unarchive(id: String) async throws -> Workspace {
        let workspace = try await client.unarchiveWorkspace(id: id)
        merge(workspace)
        scheduleRefresh()
        return workspace
    }

    /// Deletes a workspace (the server stops its sessions first, per
    /// ADR 0008 — files are never touched). Removes the row locally on
    /// success instead of merging.
    func delete(id: String) async throws {
        try await client.deleteWorkspace(id: id)
        workspaces.removeAll { $0.id == id }
        scheduleRefresh()
    }

    func workspace(id: String) -> Workspace? {
        workspaces.first { $0.id == id }
    }

    private func merge(_ workspace: Workspace) {
        if let index = workspaces.firstIndex(where: { $0.id == workspace.id }) {
            workspaces[index] = workspace
        } else {
            workspaces.insert(workspace, at: 0)
        }
    }
}

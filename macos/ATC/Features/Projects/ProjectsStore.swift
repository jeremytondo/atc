import Foundation
import Observation
import OSLog
import ATCAPI

private let logger = Logger(subsystem: "ElevenIdeas.atc", category: "projects")

/// Shared domain state for the project list. Same polling story as
/// `SessionsStore` — the server has no push events yet, and
/// `ConnectionRuntime` owns the poll task; this store only refreshes on
/// demand.
@Observable
final class ProjectsStore {
    var client: any ATCClient {
        didSet {
            projects = []
            lastError = nil
            scheduleRefresh()
        }
    }

    /// Always includes archived projects; surfaces filter locally (the
    /// Dashboard's Show Archived toggle).
    private(set) var projects: [Project] = []
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
            let fetched = try await client.projects(includeArchived: true)
            guard generation == refreshGeneration else { return }
            projects = fetched
            lastError = nil
            logger.debug("refreshed \(self.projects.count) projects")
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

    /// Creates a project; throws so the create sheet can show inline errors.
    @discardableResult
    func create(name: String, workingDir: String) async throws -> Project {
        let project = try await client.createProject(name: name, workingDir: workingDir)
        merge(project)
        scheduleRefresh()
        return project
    }

    @discardableResult
    func rename(id: String, name: String) async throws -> Project {
        let project = try await client.renameProject(id: id, name: name)
        merge(project)
        scheduleRefresh()
        return project
    }

    @discardableResult
    func archive(id: String) async throws -> Project {
        let project = try await client.archiveProject(id: id)
        merge(project)
        scheduleRefresh()
        return project
    }

    @discardableResult
    func unarchive(id: String) async throws -> Project {
        let project = try await client.unarchiveProject(id: id)
        merge(project)
        scheduleRefresh()
        return project
    }

    /// Deletes a project record (server-allowed only once it has zero
    /// workspaces). Removes the row locally on success instead of merging.
    func delete(id: String) async throws {
        try await client.deleteProject(id: id)
        projects.removeAll { $0.id == id }
        scheduleRefresh()
    }

    func project(id: String) -> Project? {
        projects.first { $0.id == id }
    }

    private func merge(_ project: Project) {
        if let index = projects.firstIndex(where: { $0.id == project.id }) {
            projects[index] = project
        } else {
            projects.insert(project, at: 0)
        }
    }
}

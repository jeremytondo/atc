import Foundation
import Observation
import OSLog
import AtelierCodeAPI

private let logger = Logger(subsystem: "ElevenIdeas.AtelierCode", category: "projects")

/// Shared domain state for the project list. Same polling story as
/// `SessionsStore` — the server has no push events yet.
@Observable
final class ProjectsStore {
    var client: any AtelierCodeClient {
        didSet {
            projects = []
            lastError = nil
            Task { await refresh() }
        }
    }

    private(set) var projects: [Project] = []
    private(set) var isLoading = false
    private(set) var hasLoadedOnce = false
    var lastError: String?
    var includeArchived = false {
        didSet { Task { await refresh() } }
    }

    /// Monotonic token: a slow in-flight refresh must not clobber the
    /// result of a newer one (e.g. a stale includeArchived=true response
    /// landing after the toggle turned off).
    private var refreshGeneration = 0

    init(client: any AtelierCodeClient) {
        self.client = client
    }

    func refresh() async {
        refreshGeneration += 1
        let generation = refreshGeneration
        isLoading = true
        defer {
            isLoading = false
            hasLoadedOnce = true
        }
        do {
            let fetched = try await client.projects(includeArchived: includeArchived)
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

    /// ~7s polling loop; run from a root `.task {}` so it auto-cancels.
    func pollLoop() async {
        while !Task.isCancelled {
            await refresh()
            try? await Task.sleep(for: .seconds(7))
        }
    }

    // MARK: - Actions

    /// Creates a project; throws so the create sheet can show inline errors.
    @discardableResult
    func create(name: String, workingDir: String) async throws -> Project {
        let project = try await client.createProject(name: name, workingDir: workingDir)
        merge(project)
        Task { await refresh() }
        return project
    }

    @discardableResult
    func rename(id: String, name: String) async throws -> Project {
        let project = try await client.renameProject(id: id, name: name)
        merge(project)
        Task { await refresh() }
        return project
    }

    @discardableResult
    func archive(id: String) async throws -> Project {
        let project = try await client.archiveProject(id: id)
        merge(project)
        Task { await refresh() }
        return project
    }

    @discardableResult
    func unarchive(id: String) async throws -> Project {
        let project = try await client.unarchiveProject(id: id)
        merge(project)
        Task { await refresh() }
        return project
    }

    func project(id: String) -> Project? {
        projects.first { $0.id == id }
    }

    private func merge(_ project: Project) {
        if let index = projects.firstIndex(where: { $0.id == project.id }) {
            // A just-archived project drops out of the default filter.
            if project.isArchived && !includeArchived {
                projects.remove(at: index)
            } else {
                projects[index] = project
            }
        } else {
            projects.insert(project, at: 0)
        }
    }
}

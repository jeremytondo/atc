// Shared domain state for one Connection's project list. Refreshes are
// driven by the runtime's SSE coordinator (no polling); mutations are never
// optimistic — state changes only from server responses and refreshes.

import ATCAppServerAPI
import Foundation
import Observation

@Observable
final class ProjectsStore {
    let client: any APIProtocol

    /// Projects, newest first.
    private(set) var projects: [Project] = []
    private let refreshState = RefreshState(category: "projects")

    var hasLoadedOnce: Bool { refreshState.hasLoadedOnce }
    var lastError: String? { refreshState.lastError }
    var isResolved: Bool { refreshState.isResolved }

    init(client: any APIProtocol) {
        self.client = client
    }

    func refresh() async {
        await refreshState.run {
            try await client.listProjects().ok.body.json
        } apply: { fetched in
            projects = fetched
        }
    }

    func project(id: String) -> Project? {
        projects.first { $0.id == id }
    }

    // MARK: - Mutations

    @discardableResult
    func create(name: String, defaultWorkingDirectory: String) async throws -> Project {
        let project = try await client
            .createProject(body: .json(.init(name: name, defaultWorkingDirectory: defaultWorkingDirectory)))
            .ok.body.json
        merge(project)
        return project
    }

    @discardableResult
    func rename(id: String, name: String) async throws -> Project {
        let project = try await client
            .updateProject(path: .init(projectId: id), body: .json(.init(name: name)))
            .ok.body.json
        merge(project)
        return project
    }

    /// Server-refused while the project still owns threads or terminals.
    func delete(id: String) async throws {
        _ = try await client.deleteProject(path: .init(projectId: id)).noContent
        refreshState.invalidateInFlight()
        projects.removeAll { $0.id == id }
    }

    private func merge(_ project: Project) {
        refreshState.invalidateInFlight()
        if let index = projects.firstIndex(where: { $0.id == project.id }) {
            projects[index] = project
        } else {
            let index = projects.firstIndex { $0.createdAt < project.createdAt }
            projects.insert(project, at: index ?? projects.endIndex)
        }
    }
}

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
        let project: Project
        switch try await client
            .createProject(body: .json(.init(name: name, defaultWorkingDirectory: defaultWorkingDirectory)))
        {
        case .ok(let ok): project = try ok.body.json
        case .unprocessableContent(let failure):
            let payload = try failure.body.json
            throw ServerError(anyOf: payload.value1, payload.value2)
        case .undocumented(statusCode: let status, _): throw ServerError.undocumented(status: status)
        }
        merge(project)
        return project
    }

    @discardableResult
    func rename(id: String, name: String) async throws -> Project {
        let project: Project
        switch try await client.updateProject(path: .init(projectId: id), body: .json(.init(name: name))) {
        case .ok(let ok): project = try ok.body.json
        case .notFound(let failure): throw ServerError(try failure.body.json)
        case .unprocessableContent(let failure):
            let payload = try failure.body.json
            throw ServerError(anyOf: payload.value1, payload.value2)
        case .undocumented(statusCode: let status, _): throw ServerError.undocumented(status: status)
        }
        merge(project)
        return project
    }

    /// Deletion cascades server-side to every owned thread and terminal.
    /// Modeled failures surface their payload message (a zmx outage
    /// mid-cascade is retryable — already-deleted children stay deleted).
    func delete(id: String) async throws {
        switch try await client.deleteProject(path: .init(projectId: id)) {
        case .noContent:
            break
        case .notFound(let failure): throw ServerError(try failure.body.json)
        case .serviceUnavailable(let failure): throw ServerError(try failure.body.json)
        case .undocumented(statusCode: let status, _): throw ServerError.undocumented(status: status)
        }
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

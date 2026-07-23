import Foundation
import Observation

struct ProjectStartupKey: Codable, Hashable {
    let connectionID: UUID
    let projectID: String
}

/// Local-only Workspace startup preferences. One versioned JSON value keeps
/// connection defaults and explicit Project overrides atomic and easy to
/// discard safely if a future or corrupted payload cannot be decoded.
@MainActor
@Observable
final class WorkspaceStartupStore {
    private struct PersistedState: Codable {
        var connectionDefaults: [UUID: StartupConfiguration] = [:]
        // Key presence alone marks a Project as Custom; an empty value is the
        // valid explicitly-empty override that suppresses Connection defaults.
        var projectOverrides: [ProjectStartupKey: StartupConfiguration] = [:]
    }

    private static let key = "workspaceStartup.v1"

    @ObservationIgnored private let defaults: UserDefaults
    private var connectionDefaults: [UUID: StartupConfiguration]
    private var projectOverrides: [ProjectStartupKey: StartupConfiguration]

    init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
        let state = Self.load(from: defaults)
        connectionDefaults = state.connectionDefaults
        projectOverrides = state.projectOverrides
    }

    func connectionConfiguration(connectionID: UUID) -> StartupConfiguration {
        connectionDefaults[connectionID] ?? .empty
    }

    func projectMode(connectionID: UUID, projectID: String) -> ProjectStartupMode {
        projectOverrides[ProjectStartupKey(
            connectionID: connectionID,
            projectID: projectID
        )] == nil ? .useConnectionDefaults : .custom
    }

    func resolvedConfiguration(
        connectionID: UUID,
        projectID: String
    ) -> StartupConfiguration {
        let key = ProjectStartupKey(connectionID: connectionID, projectID: projectID)
        if let configuration = projectOverrides[key] {
            return configuration
        }
        return connectionConfiguration(connectionID: connectionID)
    }

    func setProjectMode(
        _ mode: ProjectStartupMode,
        connectionID: UUID,
        projectID: String
    ) {
        let key = ProjectStartupKey(connectionID: connectionID, projectID: projectID)
        switch mode {
        case .useConnectionDefaults:
            guard projectOverrides.removeValue(forKey: key) != nil else { return }
        case .custom:
            guard projectOverrides[key] == nil else { return }
            projectOverrides[key] = connectionConfiguration(connectionID: connectionID)
        }
        persist()
    }

    func updateConnectionConfiguration(
        connectionID: UUID,
        _ update: (inout StartupConfiguration) -> Void
    ) {
        var configuration = connectionConfiguration(connectionID: connectionID)
        update(&configuration)
        if configuration == .empty {
            connectionDefaults.removeValue(forKey: connectionID)
        } else {
            connectionDefaults[connectionID] = configuration
        }
        persist()
    }

    func updateProjectConfiguration(
        connectionID: UUID,
        projectID: String,
        _ update: (inout StartupConfiguration) -> Void
    ) {
        let key = ProjectStartupKey(connectionID: connectionID, projectID: projectID)
        guard var configuration = projectOverrides[key] else { return }
        update(&configuration)
        // Keep the explicit override even when empty: custom empty suppresses
        // the Connection defaults.
        projectOverrides[key] = configuration
        persist()
    }

    func removeConnection(connectionID: UUID) {
        let removedDefault = connectionDefaults.removeValue(forKey: connectionID) != nil
        let previousCount = projectOverrides.count
        projectOverrides = projectOverrides.filter { $0.key.connectionID != connectionID }
        guard removedDefault || projectOverrides.count != previousCount else { return }
        persist()
    }

    func removeProject(connectionID: UUID, projectID: String) {
        let key = ProjectStartupKey(connectionID: connectionID, projectID: projectID)
        guard projectOverrides.removeValue(forKey: key) != nil else { return }
        persist()
    }

    private static func load(from defaults: UserDefaults) -> PersistedState {
        guard let data = defaults.data(forKey: key),
              let state = try? JSONDecoder().decode(PersistedState.self, from: data)
        else { return PersistedState() }
        return state
    }

    private func persist() {
        let state = PersistedState(
            connectionDefaults: connectionDefaults,
            projectOverrides: projectOverrides
        )
        guard let data = try? JSONEncoder().encode(state) else { return }
        defaults.set(data, forKey: Self.key)
    }
}

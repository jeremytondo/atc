import Foundation
import Observation
import CockpitAPI

/// Root domain model: owns settings, the API client, and the stores.
/// Rebuilds the client when server settings change.
@Observable
final class AppModel {
    let settings: AppSettings
    let connections: ConnectionsStore
    private(set) var client: any CockpitClient
    let sessions: SessionsStore
    let projects: ProjectsStore

    /// Live terminal attaches by session ID. Connections and surfaces stay
    /// alive here while the user switches around the sidebar.
    private(set) var terminals: [String: TerminalSessionController] = [:]

    init(
        settings: AppSettings = AppSettings(),
        client: (any CockpitClient)? = nil,
        connections: ConnectionsStore? = nil
    ) {
        self.settings = settings
        self.connections = connections ?? ConnectionsStore()
        let resolved = client ?? Self.makeClient(settings: settings)
        self.client = resolved
        self.sessions = SessionsStore(client: resolved)
        self.projects = ProjectsStore(client: resolved)
    }

    /// Reachability of a Connection for the Settings status dot.
    /// Phase 3 wires this to per-connection runtimes; until then everything
    /// reads as `.unknown`.
    func reachability(of id: UUID) -> Reachability {
        .unknown
    }

    /// Call after server URL or token changes.
    func rebuildClient() {
        client = Self.makeClient(settings: settings)
        sessions.client = client
        projects.client = client
        // Existing attaches keep their old endpoint until disconnected.
    }

    // MARK: - Terminal registry

    func attachIfNeeded(to session: Session) {
        guard session.attachable, terminals[session.id] == nil else { return }
        terminals[session.id] = TerminalSessionController(sessionID: session.id, client: client)
    }

    func disconnectTerminal(id: String) {
        terminals[id]?.disconnect()
        terminals.removeValue(forKey: id)
    }

    private static func makeClient(settings: AppSettings) -> any CockpitClient {
        let url = settings.serverURL ?? URL(string: AppSettings.defaultServerURLString)!
        let server = CockpitServer(baseURL: url, token: settings.token)
        return HTTPCockpitClient(server: server)
    }
}

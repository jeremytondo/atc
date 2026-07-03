import Foundation
import Observation
import CockpitAPI

/// Root domain model: owns settings, the API client, and the stores.
/// Rebuilds the client when server settings change.
@Observable
final class AppModel {
    let settings: AppSettings
    private(set) var client: any CockpitClient
    let sessions: SessionsStore

    init(settings: AppSettings = AppSettings(), client: (any CockpitClient)? = nil) {
        self.settings = settings
        let resolved = client ?? Self.makeClient(settings: settings)
        self.client = resolved
        self.sessions = SessionsStore(client: resolved)
    }

    /// Call after server URL or token changes.
    func rebuildClient() {
        client = Self.makeClient(settings: settings)
        sessions.client = client
    }

    private static func makeClient(settings: AppSettings) -> any CockpitClient {
        let url = settings.serverURL ?? URL(string: AppSettings.defaultServerURLString)!
        let server = CockpitServer(baseURL: url, token: settings.token)
        return HTTPCockpitClient(server: server)
    }
}

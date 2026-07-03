import Foundation

/// The interface the app depends on for all Cockpit REST calls.
/// `HTTPCockpitClient` is the real implementation; previews and tests
/// inject mocks.
public protocol CockpitClient: Sendable {
    func health() async throws -> Health
    func version() async throws -> Version
    func sessions(includeArchived: Bool, status: SessionStatus?) async throws -> [Session]
    func session(id: String) async throws -> SessionDetail
    func startSession(_ request: StartSessionRequest) async throws -> SessionDetail
    func terminateSession(id: String) async throws -> SessionDetail
    func archiveSession(id: String) async throws -> SessionDetail
    func sendText(sessionID: String, text: String) async throws
    func sendKey(sessionID: String, key: String) async throws
    func actions() async throws -> [CockpitAction]
    func environments() async throws -> [CockpitEnvironment]

    /// WebSocket URL for `GET /api/sessions/{id}/attach`.
    func attachURL(sessionID: String) -> URL
    /// Extra headers (e.g. bearer auth) the attach WebSocket must send.
    func attachHeaders() -> [String: String]
}

public extension CockpitClient {
    func sessions() async throws -> [Session] {
        try await sessions(includeArchived: false, status: nil)
    }
}

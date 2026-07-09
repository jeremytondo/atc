import Foundation
import Observation
import OSLog
import AtelierCodeAPI

private let logger = Logger(subsystem: "ElevenIdeas.AtelierCode", category: "sessions")

/// Shared domain state for the session list. Polling is the whole sync
/// story — the server has no SSE.
@Observable
final class SessionsStore {
    var client: any AtelierCodeClient {
        didSet {
            sessions = []
            lastError = nil
            Task { await refresh() }
        }
    }

    private(set) var sessions: [Session] = []
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
            // Always fetch archived too when toggled; the server filters,
            // the view groups.
            let fetched = try await client.sessions(includeArchived: includeArchived, status: nil)
            guard generation == refreshGeneration else { return }
            sessions = fetched
            lastError = nil
            logger.debug("refreshed \(self.sessions.count) sessions")
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

    /// Starts a session; throws so the create sheet can show inline errors.
    @discardableResult
    func start(_ request: StartSessionRequest) async throws -> SessionDetail {
        let detail = try await client.startSession(request)
        merge(detail)
        Task { await refresh() }
        return detail
    }

    @discardableResult
    func terminate(id: String) async throws -> SessionDetail {
        let detail = try await client.terminateSession(id: id)
        merge(detail)
        Task { await refresh() }
        return detail
    }

    @discardableResult
    func archive(id: String) async throws -> SessionDetail {
        let detail = try await client.archiveSession(id: id)
        merge(detail)
        Task { await refresh() }
        return detail
    }

    func session(id: String) -> Session? {
        sessions.first { $0.id == id }
    }

    private func merge(_ detail: SessionDetail) {
        let session = detail.asSession
        if let index = sessions.firstIndex(where: { $0.id == session.id }) {
            // A just-archived session drops out of the default filter.
            if session.isArchived && !includeArchived {
                sessions.remove(at: index)
            } else {
                sessions[index] = session
            }
        } else {
            sessions.insert(session, at: 0)
        }
    }
}

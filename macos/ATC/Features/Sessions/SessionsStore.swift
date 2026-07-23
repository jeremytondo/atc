import Foundation
import Observation
import OSLog
import ATCAPI

private let logger = Logger(subsystem: "ElevenIdeas.atc", category: "sessions")

/// Shared domain state for the session list. Polling is the whole sync
/// story — the server has no SSE — and `ConnectionRuntime` owns the poll
/// task; this store only refreshes on demand.
@Observable
final class SessionsStore {
    var client: any ATCClient {
        didSet {
            sessions = []
            lastError = nil
            scheduleRefresh()
        }
    }

    /// Complete server list, including both Live and Ended sessions.
    private(set) var sessions: [Session] = []
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
            let fetched = try await client.sessions(status: nil)
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

    private func scheduleRefresh() {
        followUpRefresh?.cancel()
        followUpRefresh = Task { await refresh() }
    }

    // MARK: - Mutations

    /// Starts a session; throws so the create sheet can show inline errors.
    @discardableResult
    func start(_ request: StartSessionRequest) async throws -> Session {
        let session = try await client.startSession(request)
        merge(session)
        scheduleRefresh()
        return session
    }

    @discardableResult
    func rename(id: String, name: String) async throws -> Session {
        let session = try await client.renameSession(id: id, name: name)
        merge(session)
        scheduleRefresh()
        return session
    }

    /// Deletes a session's metadata (the server ends it first if Live —
    /// files are never touched). Removes the row locally on
    /// success instead of merging.
    func delete(id: String) async throws {
        try await client.deleteSession(id: id)
        sessions.removeAll { $0.id == id }
        scheduleRefresh()
    }

    func session(id: String) -> Session? {
        sessions.first { $0.id == id }
    }

    /// Applies the server's authoritative stale-interaction result without
    /// waiting for the next poll. A follow-up refresh fills in server time.
    func reconcileEnded(id: String) {
        guard let index = sessions.firstIndex(where: { $0.id == id }) else { return }
        sessions[index].status = .ended
        scheduleRefresh()
    }

    private func merge(_ session: Session) {
        if let index = sessions.firstIndex(where: { $0.id == session.id }) {
            sessions[index] = session
        } else {
            sessions.insert(session, at: 0)
        }
    }
}

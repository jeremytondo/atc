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

    private(set) var sessions: [Session] = []
    private(set) var isLoading = false
    private(set) var hasLoadedOnce = false
    var lastError: String?
    var includeArchived = false {
        didSet { scheduleRefresh() }
    }

    /// Monotonic token: a slow in-flight refresh must not clobber the
    /// result of a newer one (e.g. a stale includeArchived=true response
    /// landing after the toggle turned off).
    private var refreshGeneration = 0

    /// The one owned follow-up refresh (post-mutation or filter change);
    /// superseded follow-ups are cancelled instead of piling up.
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

    private func scheduleRefresh() {
        followUpRefresh?.cancel()
        followUpRefresh = Task { await refresh() }
    }

    // MARK: - Actions

    /// Starts a session; throws so the create sheet can show inline errors.
    @discardableResult
    func start(_ request: StartSessionRequest) async throws -> SessionDetail {
        let detail = try await client.startSession(request)
        merge(detail)
        scheduleRefresh()
        return detail
    }

    @discardableResult
    func terminate(id: String) async throws -> SessionDetail {
        let detail = try await client.terminateSession(id: id)
        merge(detail)
        scheduleRefresh()
        return detail
    }

    @discardableResult
    func archive(id: String) async throws -> SessionDetail {
        let detail = try await client.archiveSession(id: id)
        merge(detail)
        scheduleRefresh()
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

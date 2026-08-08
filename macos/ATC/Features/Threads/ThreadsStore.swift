// Shared domain state for one Connection's threads: the active list, the
// lazily-loaded global archived list, and every thread mutation. Refreshes
// are driven by the runtime's SSE coordinator (no polling); the archived
// list is only fetched once the UI has asked for it, because thread listings
// are priced (each consults the multiplexer for linked terminals).
//
// Mutations are never optimistic: state changes only from server responses
// (`merge`) and refreshes, so a failed request leaves presentation intact.

import ATCAppServerAPI
import Foundation
import Observation

@Observable
final class ThreadsStore {
    let client: any APIProtocol

    /// Active (non-archived) threads, newest first.
    private(set) var threads: [ATCThread] = []
    /// Archived threads, newest first; empty until first loaded.
    private(set) var archivedThreads: [ATCThread] = []
    /// The archived collection is fetched on demand; once loaded it stays
    /// part of every refresh so SSE invalidations keep it current.
    private(set) var hasLoadedArchivedOnce = false
    private let refreshState = RefreshState(category: "threads")

    var hasLoadedOnce: Bool { refreshState.hasLoadedOnce }
    var lastError: String? { refreshState.lastError }
    var isResolved: Bool { refreshState.isResolved }

    init(client: any APIProtocol) {
        self.client = client
    }

    func refresh() async {
        let includeArchived = hasLoadedArchivedOnce
        await refreshState.run {
            async let activeList = client.listThreads(query: .init()).ok.body.json
            let archivedList = includeArchived
                ? try await client.listThreads(query: .init(archived: ._true)).ok.body.json
                : nil
            return (active: try await activeList, archived: archivedList)
        } apply: { fetched in
            threads = fetched.active
            if let archived = fetched.archived {
                archivedThreads = archived
            }
        }
    }

    /// First use of the Archived filter; afterwards `refresh()` covers it.
    /// A failed first load resets the flag so the next filter visit retries
    /// instead of presenting an affirmative empty archive.
    func loadArchivedIfNeeded() async {
        guard !hasLoadedArchivedOnce else { return }
        hasLoadedArchivedOnce = true
        await refresh()
        if lastError != nil {
            hasLoadedArchivedOnce = false
        }
    }

    func thread(id: String) -> ATCThread? {
        threads.first { $0.id == id } ?? archivedThreads.first { $0.id == id }
    }

    // MARK: - Mutations (server-confirmed, then reconciled by SSE)

    @discardableResult
    func create(_ request: Components.Schemas.CreateThreadRequest) async throws -> ATCThread {
        let thread = try await client.createThread(body: .json(request)).ok.body.json
        merge(thread)
        return thread
    }

    @discardableResult
    func rename(id: String, name: String) async throws -> ATCThread {
        let thread = try await client
            .updateThread(path: .init(threadId: id), body: .json(.init(name: name)))
            .ok.body.json
        merge(thread)
        return thread
    }

    @discardableResult
    func pin(id: String) async throws -> ATCThread {
        let thread = try await client.pinThread(path: .init(threadId: id)).ok.body.json
        merge(thread)
        return thread
    }

    @discardableResult
    func unpin(id: String) async throws -> ATCThread {
        let thread = try await client.unpinThread(path: .init(threadId: id)).ok.body.json
        merge(thread)
        return thread
    }

    @discardableResult
    func archive(id: String) async throws -> ATCThread {
        let thread = try await client.archiveThread(path: .init(threadId: id)).ok.body.json
        merge(thread)
        return thread
    }

    @discardableResult
    func unarchive(id: String) async throws -> ATCThread {
        let thread = try await client.unarchiveThread(path: .init(threadId: id)).ok.body.json
        merge(thread)
        return thread
    }

    func delete(id: String) async throws {
        _ = try await client.deleteThread(path: .init(threadId: id)).noContent
        refreshState.invalidateInFlight()
        threads.removeAll { $0.id == id }
        archivedThreads.removeAll { $0.id == id }
    }

    /// Idempotent open of the thread's TUI terminal: a live linked terminal
    /// is returned as-is, otherwise the provider TUI is (re)launched.
    func openTerminal(threadID: String) async throws -> Terminal {
        try await client.openThreadTerminal(path: .init(threadId: threadID)).ok.body.json
    }

    /// Places a server-returned thread into whichever list its archive state
    /// says it belongs to, preserving newest-first order by creation time.
    private func merge(_ thread: ATCThread) {
        refreshState.invalidateInFlight()
        threads.removeAll { $0.id == thread.id }
        archivedThreads.removeAll { $0.id == thread.id }
        if thread.isArchived {
            let index = archivedThreads.firstIndex { $0.archivedAt ?? .distantPast < thread.archivedAt ?? .distantPast }
            archivedThreads.insert(thread, at: index ?? archivedThreads.endIndex)
        } else {
            let index = threads.firstIndex { $0.createdAt < thread.createdAt }
            threads.insert(thread, at: index ?? threads.endIndex)
        }
    }
}

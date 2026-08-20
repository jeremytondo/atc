// One thread's Chat state: the transcript copy (`ChatTranscript`) plus the
// stream and requests that keep it current. Owned by the thread's
// `ConnectionRuntime` for as long as any window shows the thread in Chat, so
// two windows on one thread share one copy and one subscription.
//
// Load and resume, all driven by the stream so there is one path:
// - The subscription starts at once. On every `.connected` the copy is
//   loaded if this subscription asked for no `after` (nothing was replayed:
//   the copy is empty, or a reconnect raced the first read) or is invalid,
//   then the live-only state (requests, queue) is refetched — and refetched
//   again on a delay while either read fails, because those events are
//   never replayed.
// - `after` is the highest seq applied, asked on every reconnect, so a lost
//   connection replays exactly the gap; the copy is never refetched for a
//   plain reconnect and the transcript stays on screen.
// - `snapshot.invalidated` marks the copy invalid and refetches the newest
//   page in place. Until a page lands — likewise while the copy was never
//   loaded — durable events are not applied and the cursor does not move (a
//   reconnect meanwhile is answered with the invalidation again, never a
//   silent resume of a dead copy); the stream itself stays up — replay is
//   only ever for a gap.
// - Every read is serialized with event handling: the stream loop waits for
//   an in-flight page or live-state read before applying anything, so a page
//   can never land beneath a change this connection already applied and a
//   request list can never resurrect a request the stream already closed.
//   Reads happen at the server after any change already applied here, so
//   `page.seq` is never behind the cursor.
//
// Rendering state (ATC-214): the reducer's copy is deliberately NOT observed
// — a streamed delta arrives many times a second, and invalidating the whole
// pane per delta is the jank ATC-212 complained about. What views observe
// are the projections here: `rows` (rebuilt inside withAnimation on
// structural change only, boxes reused so disclosure state survives),
// per-item `ChatItemModel` boxes (a delta touches exactly one), and the
// small live-state mirrors (requests, queue, runningTurn…).
//
// Mutations go straight to the server and never touch the copy — what the
// user did comes back through the stream like anyone else's action — except
// the one optimistic touch: a sent prompt echoes as a pending row instantly
// and resolves when the server shows it somewhere real. `perform` is the
// action seam: gated on this stream being live (not the app-wide connection
// dot) and reporting failures inline (`actionError`), never as a modal.

import ATCAppServerAPI
import ATCAppServerTransport
import Foundation
import Observation
import SwiftUI

@Observable
final class ThreadChatModel {
    typealias StreamFactory =
        (_ after: @escaping @Sendable () -> Int?) -> AsyncStream<ThreadEventStream.Event>

    enum ConnectionPhase: Equatable {
        case connecting
        case live
        case reconnecting
    }

    /// Delay before re-reading requests/queue after a failed read.
    var liveStateRetryDelay: Duration = .seconds(2)

    let threadID: String
    /// The reducer's copy — the source the projections below derive from.
    /// Unobserved on purpose (see the header's rendering-state rules).
    @ObservationIgnored private(set) var transcript = ChatTranscript()
    private(set) var connection: ConnectionPhase = .connecting
    /// The last newest-page read failed; the copy shown is empty (never
    /// loaded) or invalid (a provider re-read replaced it) until it succeeds.
    private(set) var loadError: String?
    private(set) var isLoadingOlder = false
    /// The last `promptThread` refusal, cleared by the next send.
    private(set) var promptError: String?
    /// The last chat action's failure, shown inline above the composer.
    private(set) var actionError: String?
    /// The last prompt this client sent (⌘↑ recalls it into the composer).
    private(set) var lastSentPrompt: String?

    // What the views observe, projected from `transcript` after each change.
    private(set) var rows: [ChatRowModel] = []
    private(set) var requests: [ThreadRequest] = []
    private(set) var queue: [QueuedPrompt] = []
    private(set) var runningTurn: ThreadTurn?
    private(set) var hasMore = false
    private(set) var isLoaded = false

    private let client: any APIProtocol
    private let makeStream: StreamFactory
    private let cursor = SeqCursor()
    @ObservationIgnored private var boxes: [String: ChatItemModel] = [:]
    private var streamTask: Task<Void, Never>?
    private var pageLoad: Task<Void, Never>?
    private var liveStateRefresh: Task<Void, Never>?
    private var liveStateRetry: Task<Void, Never>?
    /// A `snapshot.invalidated` has not yet been answered by a landed page.
    private var needsResync = false

    init(threadID: String, client: any APIProtocol, makeStream: @escaping StreamFactory) {
        self.threadID = threadID
        self.client = client
        self.makeStream = makeStream
    }

    // MARK: - Lifecycle

    func start() {
        guard streamTask == nil else { return }
        let stream = makeStream { [cursor] in cursor.ask() }
        streamTask = Task { [weak self] in
            for await event in stream {
                guard let self, !Task.isCancelled else { return }
                await self.handle(event)
            }
        }
    }

    func stop() {
        for task in [streamTask, pageLoad, liveStateRefresh, liveStateRetry] {
            task?.cancel()
        }
        streamTask = nil
        pageLoad = nil
        liveStateRefresh = nil
        liveStateRetry = nil
    }

    private func handle(_ event: ThreadEventStream.Event) async {
        // Never apply across an in-flight read (see header).
        await pageLoad?.value
        await liveStateRefresh?.value
        switch event {
        case .connected:
            connection = .live
            if cursor.lastAsked == nil || needsResync {
                await loadNewest()
            }
            await refreshLiveState()
        case .disconnected:
            connection = .reconnecting
        case .event(let event):
            if needsResync || !transcript.isLoaded, event.seq != nil {
                // The copy is dead or missing: a durable change is only
                // ever picked up by the page, so retry the read instead of
                // applying it.
                await loadNewest()
                return
            }
            let mutation = transcript.apply(event)
            if mutation == .invalidated {
                needsResync = true
                await loadNewest()
                return
            }
            project(mutation)
            cursor.seq = transcript.seq
        }
    }

    // MARK: - Projection

    /// Applies one reducer outcome to the observed state. Structural and
    /// live-state changes animate (rows slide in, the bottom bar resizes);
    /// a delta only touches its item's box — never animated, never a rebuild.
    private func project(_ mutation: ChatMutation) {
        switch mutation {
        case .none, .invalidated:
            return
        case .item(let id):
            guard let item = transcript.items.first(where: { $0.id == id }) else { return }
            boxes[id]?.update(item)
        case .structure:
            withAnimation(.snappy(duration: 0.25)) {
                rows = ChatRowBuilder.rows(from: transcript, reusing: &boxes)
                projectLiveState()
            }
        case .liveState:
            withAnimation(.snappy(duration: 0.25)) { projectLiveState() }
        }
    }

    private func projectLiveState() {
        if requests != transcript.requests { requests = transcript.requests }
        if queue != transcript.queue { queue = transcript.queue }
        if runningTurn != transcript.runningTurn { runningTurn = transcript.runningTurn }
        if hasMore != transcript.hasMore { hasMore = transcript.hasMore }
        if isLoaded != transcript.isLoaded { isLoaded = transcript.isLoaded }
    }

    // MARK: - Reads

    /// (Re)reads the newest page. Concurrent callers share one read; on
    /// failure the previous copy (if any) stays and `loadError` is set.
    func loadNewest() async {
        if let pageLoad {
            await pageLoad.value
            return
        }
        let load = Task {
            do {
                let page = try await fetchTranscript(before: nil)
                guard !Task.isCancelled else { return }
                transcript.load(page)
                cursor.seq = page.seq
                needsResync = false
                loadError = nil
                project(.structure)
            } catch {
                guard !Task.isCancelled else { return }
                loadError = error.localizedDescription
            }
        }
        pageLoad = load
        await load.value
        pageLoad = nil
    }

    /// The page before the oldest item on screen. A cursor from a replaced
    /// copy reads as an empty page (or is discarded by the reducer when the
    /// copy changed under it), which simply ends paging.
    func loadOlder() async throws {
        guard transcript.hasMore, !isLoadingOlder, let first = transcript.items.first else { return }
        isLoadingOlder = true
        defer { isLoadingOlder = false }
        transcript.prependOlder(try await fetchTranscript(before: first.id))
        project(.structure)
    }

    /// Requests and the queue are live-only (never replayed): refetched on
    /// every connect, and again after a delay while either read fails —
    /// otherwise a pending approval could stay hidden for as long as the
    /// stream stays healthy.
    private func refreshLiveState() async {
        if let liveStateRefresh {
            await liveStateRefresh.value
            return
        }
        liveStateRetry?.cancel()
        liveStateRetry = nil
        let refresh = Task {
            async let requests = try? client.listThreadRequests(path: .init(threadId: threadID)).ok.body.json
            async let queue = try? client.listThreadQueue(path: .init(threadId: threadID)).ok.body.json
            let (readRequests, readQueue) = await (requests, queue)
            guard !Task.isCancelled else { return }
            if let readRequests { transcript.replaceRequests(readRequests) }
            if let readQueue { transcript.replaceQueue(readQueue) }
            // The queue read may have settled a pending echo: rebuild rows too.
            project(.structure)
            guard readRequests == nil || readQueue == nil else { return }
            liveStateRetry = Task { [weak self] in
                try? await Task.sleep(for: self?.liveStateRetryDelay ?? .seconds(2))
                guard !Task.isCancelled else { return }
                await self?.refreshLiveState()
            }
        }
        liveStateRefresh = refresh
        await refresh.value
        liveStateRefresh = nil
    }

    private func fetchTranscript(before: String?) async throws -> ThreadTranscriptPage {
        switch try await client.getThreadTranscript(path: .init(threadId: threadID), query: .init(before: before)) {
        case .ok(let ok): return try ok.body.json
        case .notFound(let failure): throw ServerError(try failure.body.json)
        case .undocumented(statusCode: let status, _): throw ServerError.undocumented(status: status)
        }
    }

    // MARK: - Actions

    /// The one seam chat actions run through: refused inline while this
    /// thread's stream is not live (the app-wide dot says nothing about this
    /// stream), and failures land in `actionError` — never a modal.
    func perform(_ operation: @escaping () async throws -> Void) {
        guard connection == .live else {
            actionError =
                "Not connected — the thread's stream is still \(connection == .connecting ? "connecting" : "reconnecting")."
            return
        }
        actionError = nil
        Task {
            do {
                try await operation()
            } catch {
                actionError = error.localizedDescription
            }
        }
    }

    func clearActionError() {
        actionError = nil
    }

    /// Prompts the thread, echoing the prompt as a pending row at once.
    /// Returns false when the server refused (the error is in `promptError`)
    /// so the composer restores the text.
    @discardableResult
    func send(_ prompt: String) async -> Bool {
        promptError = nil
        lastSentPrompt = prompt
        let pendingID = transcript.addPending(prompt)
        project(.structure)
        do {
            switch try await client.promptThread(path: .init(threadId: threadID), body: .json(.init(prompt: prompt))) {
            case .ok(let ok):
                let response = try ok.body.json
                transcript.resolvePending(
                    id: pendingID, promptId: response.promptId, turnId: response.turnId)
                project(.structure)
                return true
            case .notFound(let failure): throw ServerError(try failure.body.json)
            case .conflict(let failure):
                let payload = try failure.body.json
                throw ServerError(anyOf: payload.value1, payload.value2)
            case .serviceUnavailable(let failure): throw ServerError(try failure.body.json)
            case .undocumented(statusCode: let status, _): throw ServerError.undocumented(status: status)
            }
        } catch {
            transcript.removePending(id: pendingID)
            project(.structure)
            promptError = error.localizedDescription
            return false
        }
    }

    /// Interrupts the server-driven turn; a TUI-driven turn is a server
    /// no-op, and queued prompts stay queued.
    func interrupt() async throws {
        switch try await client.interruptThread(path: .init(threadId: threadID)) {
        case .noContent: break
        case .notFound(let failure): throw ServerError(try failure.body.json)
        case .serviceUnavailable(let failure): throw ServerError(try failure.body.json)
        case .undocumented(statusCode: let status, _): throw ServerError.undocumented(status: status)
        }
    }

    func answer(requestID: String, _ answer: ThreadRequestAnswer) async throws {
        switch try await client.answerThreadRequest(
            path: .init(threadId: threadID, requestId: requestID), body: .json(answer))
        {
        case .noContent: break
        case .badRequest(let failure): throw ServerError(try failure.body.json)
        case .notFound(let failure):
            let payload = try failure.body.json
            throw ServerError(anyOf: payload.value1, payload.value2)
        case .serviceUnavailable(let failure): throw ServerError(try failure.body.json)
        case .undocumented(statusCode: let status, _): throw ServerError.undocumented(status: status)
        }
    }

    func withdraw(promptID: String) async throws {
        switch try await client.deleteQueuedPrompt(path: .init(threadId: threadID, promptId: promptID)) {
        case .noContent: break
        case .notFound(let failure):
            let payload = try failure.body.json
            throw ServerError(anyOf: payload.value1, payload.value2)
        case .undocumented(statusCode: let status, _): throw ServerError.undocumented(status: status)
        }
    }
}

/// The resume cursor the stream reads on every (re)connect, and what it was
/// last told — a subscription made with no `after` replays nothing, so its
/// `.connected` must load the copy. Its own lock because the stream asks
/// from off the main actor.
private nonisolated final class SeqCursor: @unchecked Sendable {
    private let lock = NSLock()
    private var value: Int?
    private var asked: Int?

    var seq: Int? {
        get { lock.withLock { value } }
        set { lock.withLock { value = newValue } }
    }

    /// The `after` handed to the subscription currently connecting.
    var lastAsked: Int? { lock.withLock { asked } }

    func ask() -> Int? {
        lock.withLock {
            asked = value
            return value
        }
    }
}

// The Chat view's copy of one thread's runtime state, and the pure reducer
// that keeps it current from the per-thread event stream. Everything here
// is synchronous and value-typed so the merge rules can be tested without a
// server; `ThreadChatModel` owns the I/O around it.
//
// Merge rules (from the contract, `ThreadEvent` / `ThreadTranscript`):
// - Items carry no position: transcript order is array order, a known id is
//   replaced in place (an update re-sends the whole item), an unknown id
//   appends at the tail. Turns upsert the same way.
// - `seq` is the highest durable change applied. A durable event at or
//   below it is already reflected — by the page it was read into or by a
//   later change — and is dropped, so a live event that overtakes a
//   transcript read can never roll a row back.
// - `text.delta` is live-only and extends the named item; a delta for an
//   item never seen is dropped (its `item.completed` carries the whole text).
// - The server persists streamed text on a throttle (durable partials), so a
//   durable update can lag the deltas already applied here: an incomplete
//   text item never rolls back to a prefix of what it already shows.
// - `snapshot.invalidated` means a provider re-read replaced the copy: the
//   caller must refetch (`apply` says so); nothing here can repair it, and
//   it does not move `seq` — only a page that actually landed may, so a
//   reconnect before the refetch succeeds is answered with the invalidation
//   again instead of silently resuming a dead copy.
// - Every page names the `snapshotVersion` it was read from; an older page
//   from a copy that has since been replaced is discarded.
// - Requests and the queue are live state, never transcript items (the
//   row builder renders a request beside the item it blocks): the queue is
//   replaced wholesale on every `queue.updated`; requests open and close
//   by id.
// - Pending prompts (the optimistic echo) are client-local rows at the tail:
//   one is added the instant a send leaves, learns its promptId (and turnId)
//   from the send response and the turn events, and resolves — removed —
//   when the queue lists its prompt or the real userMessage item lands. A
//   landing userMessage settles its echo in the same step — matched by turn,
//   or by exact text while the send response is still in flight (the stream
//   can outrun it) — reported as `.handoff` so the visually identical row
//   swap renders unanimated instead of flashing.
//
// `apply` reports what kind of change happened (`ChatMutation`) so the model
// rebuilds its row models only on structural change, never per delta.

import ATCAppServerAPI
import Foundation

/// What one reducer step changed, for the model's row bookkeeping. Only the
/// hot path (a streaming item's content) gets its own case; every other
/// accepted change is `.structure` — a rebuild is cheap and impossible to
/// misclassify.
enum ChatMutation: Equatable {
    /// Nothing visible (a dropped stale event).
    case none
    /// One item's content changed in place (a delta, an in-place update).
    case item(String)
    /// Anything else the copy accepted: rebuild rows, re-project live state.
    case structure
    /// A real userMessage replaced its pending echo in one step. The two
    /// rows render identically, so the projection must rebuild without
    /// animation — animated, SwiftUI fades one row out while sliding the
    /// other in, and the prompt visibly flashes.
    case handoff
    /// The copy was replaced server-side; the caller must refetch.
    case invalidated
}

/// A prompt sent from this client that the transcript does not carry yet.
struct PendingPrompt: Identifiable, Equatable {
    let id: String
    let text: String
    /// The images sent with it, shown from local bytes until the real item lands.
    let attachments: [PendingAttachment]
    /// The admitted prompt's id, once the send response arrived.
    var promptId: String?
    /// The turn the prompt started, once known (response or turn event).
    var turnId: String?
}

struct ChatTranscript: Equatable {
    private(set) var items: [ThreadItem] = []
    private(set) var turns: [ThreadTurn] = []
    private(set) var requests: [ThreadRequest] = []
    private(set) var queue: [QueuedPrompt] = []
    private(set) var pendingPrompts: [PendingPrompt] = []
    /// The highest durable `seq` applied; subscribe `after` it.
    private(set) var seq: Int?
    /// The provider copy the loaded pages came from.
    private(set) var snapshotVersion: Int?
    /// Older items exist before `items.first`.
    private(set) var hasMore = false
    /// A transcript page has been read at least once.
    private(set) var isLoaded = false

    /// The turn the server is driving right now, if any.
    var runningTurn: ThreadTurn? {
        turns.last { $0.status == .running }
    }

    func turn(id: String) -> ThreadTurn? {
        turns.first { $0.id == id }
    }

    /// Replaces the copy with the newest page (first load, or after a
    /// snapshot invalidation).
    mutating func load(_ page: ThreadTranscriptPage) {
        items = page.items
        turns = page.turns
        seq = page.seq
        snapshotVersion = page.snapshotVersion
        hasMore = page.hasMore
        isLoaded = true
        settlePending()
    }

    /// Prepends an older page (`before` the first item). Its turns merge;
    /// its `seq` is not newer than what is already applied. A page from a
    /// copy that was replaced meanwhile is discarded.
    mutating func prependOlder(_ page: ThreadTranscriptPage) {
        guard page.snapshotVersion == snapshotVersion else { return }
        let knownItems = Set(items.map(\.id))
        let knownTurns = Set(turns.map(\.id))
        items = page.items.filter { !knownItems.contains($0.id) } + items
        turns = page.turns.filter { !knownTurns.contains($0.id) } + turns
        hasMore = page.hasMore
        settlePending()
    }

    mutating func replaceRequests(_ requests: [ThreadRequest]) {
        self.requests = requests
    }

    mutating func replaceQueue(_ prompts: [QueuedPrompt]) {
        queue = prompts
        settlePending(queueIsFresh: true)
    }

    // MARK: - Pending prompts

    /// A send left for the server: echo it at the tail immediately.
    mutating func addPending(_ text: String, attachments: [PendingAttachment] = []) -> String {
        let pending = PendingPrompt(id: UUID().uuidString, text: text, attachments: attachments)
        pendingPrompts.append(pending)
        return pending.id
    }

    /// The send failed; the echo goes (the composer restores the text).
    mutating func removePending(id: String) {
        pendingPrompts.removeAll { $0.id == id }
    }

    /// The send response arrived: record the ids and settle — the matching
    /// queue entry or userMessage may already be here (a fast stream).
    mutating func resolvePending(id: String, promptId: String, turnId: String?) {
        guard let index = pendingPrompts.firstIndex(where: { $0.id == id }) else { return }
        pendingPrompts[index].promptId = promptId
        pendingPrompts[index].turnId = turnId
        settlePending()
    }

    /// A pending prompt resolves when the server shows it somewhere real:
    /// the queue lists its promptId (the strip takes over), or the turn it
    /// started (matched promptId → turnId) has its userMessage item. With
    /// fresh queue evidence, a prompt the server queued (a promptId, no
    /// turn) that the queue no longer lists was withdrawn — by another
    /// client — and the echo goes too.
    private mutating func settlePending(queueIsFresh: Bool = false) {
        guard !pendingPrompts.isEmpty else { return }
        matchPendingTurns()
        pendingPrompts.removeAll { pending in
            // No response yet: nothing to match against, keep the echo.
            guard let promptId = pending.promptId else { return false }
            if queue.contains(where: { $0.id == promptId }) { return true }
            if let turnId = pending.turnId {
                return items.contains { item in
                    guard case .userMessage(let message) = item else { return false }
                    return message.turnId == turnId
                }
            }
            return queueIsFresh
        }
    }

    /// A landing userMessage settles the echo it confirms in the same step:
    /// matched through its turn once the send response arrived, or by exact
    /// text while the send is still unanswered — the stream can hand over
    /// the real item before the response carries the promptId. Reports
    /// whether an echo settled so the caller can mark the mutation a
    /// handoff (rendered as an unanimated swap).
    private mutating func settle(byAppended item: ThreadItem) -> Bool {
        guard case .userMessage(let message) = item, !pendingPrompts.isEmpty else { return false }
        matchPendingTurns()
        let byTurn = pendingPrompts.firstIndex { $0.turnId != nil && $0.turnId == message.turnId }
        // Text identifies an echo only when it is unambiguous: two image-only
        // prompts in flight both read "", and the wrong one must not settle.
        let byText = pendingPrompts.indices.filter {
            pendingPrompts[$0].promptId == nil && pendingPrompts[$0].text == message.text
        }
        guard let index = byTurn ?? (byText.count == 1 ? byText[0] : nil) else { return false }
        pendingPrompts.remove(at: index)
        return true
    }

    /// Fills in the turn a resolved pending started, once that turn's event
    /// (carrying the promptId) has arrived.
    private mutating func matchPendingTurns() {
        for index in pendingPrompts.indices where pendingPrompts[index].turnId == nil {
            guard let promptId = pendingPrompts[index].promptId else { continue }
            pendingPrompts[index].turnId = turns.first { $0.promptId == promptId }?.id
        }
    }

    // MARK: - Events

    /// Applies one stream event and reports what changed.
    mutating func apply(_ event: ThreadEvent) -> ChatMutation {
        switch event {
        case .item_started(let event):
            guard advance(to: event.seq) else { return .none }
            return upsert(event.item)
        case .item_updated(let event):
            guard advance(to: event.seq) else { return .none }
            return upsert(event.item)
        case .item_completed(let event):
            guard advance(to: event.seq) else { return .none }
            return upsert(event.item)
        case .turn_started(let event):
            guard advance(to: event.seq) else { return .none }
            return upsert(event.turn)
        case .turn_completed(let event):
            guard advance(to: event.seq) else { return .none }
            return upsert(event.turn)
        case .text_delta(let event):
            guard let index = items.firstIndex(where: { $0.id == event.itemId }) else {
                return .none
            }
            items[index].appendText(event.delta)
            return .item(event.itemId)
        case .request_opened(let event):
            requests.removeAll { $0.id == event.request.id }
            requests.append(event.request)
            return .structure
        case .request_closed(let event):
            requests.removeAll { $0.id == event.requestId }
            return .structure
        case .queue_updated(let event):
            queue = event.prompts
            settlePending(queueIsFresh: true)
            return .structure
        case .snapshot_invalidated:
            return .invalidated
        }
    }

    /// Moves the durable cursor forward; false when the change is already
    /// reflected and the event must be dropped.
    private mutating func advance(to eventSeq: Int) -> Bool {
        if let seq, eventSeq <= seq { return false }
        seq = eventSeq
        return true
    }

    private mutating func upsert(_ item: ThreadItem) -> ChatMutation {
        if let index = items.firstIndex(where: { $0.id == item.id }) {
            let previous = items[index]
            items[index] = merged(item, over: previous)
            // Reparenting would move the row; anything else changed in place.
            return previous.parentItemId == item.parentItemId ? .item(item.id) : .structure
        }
        items.append(item)
        return settle(byAppended: item) ? .handoff : .structure
    }

    /// The durable-partials guard (see the header): the server's throttled
    /// persist can lag the deltas already applied, so an incomplete text
    /// item keeps the longer text it already shows when the incoming text
    /// is a strict prefix of it.
    private func merged(_ incoming: ThreadItem, over current: ThreadItem) -> ThreadItem {
        switch (incoming, current) {
        case (.assistantText(var new), .assistantText(let old)):
            guard !new.complete, old.text.count > new.text.count, old.text.hasPrefix(new.text)
            else { return incoming }
            new.text = old.text
            return .assistantText(new)
        case (.reasoning(var new), .reasoning(let old)):
            guard !new.complete, old.text.count > new.text.count, old.text.hasPrefix(new.text)
            else { return incoming }
            new.text = old.text
            return .reasoning(new)
        default:
            return incoming
        }
    }

    private mutating func upsert(_ turn: ThreadTurn) -> ChatMutation {
        if let index = turns.firstIndex(where: { $0.id == turn.id }) {
            turns[index] = turn
        } else {
            turns.append(turn)
        }
        settlePending()
        return .structure
    }
}

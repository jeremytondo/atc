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
// - `snapshot.invalidated` means a provider re-read replaced the copy: the
//   caller must refetch (`apply` says so); nothing here can repair it, and
//   it does not move `seq` — only a page that actually landed may, so a
//   reconnect before the refetch succeeds is answered with the invalidation
//   again instead of silently resuming a dead copy.
// - Every page names the `snapshotVersion` it was read from; an older page
//   from a copy that has since been replaced is discarded.
// - Requests and the queue are live state, never transcript rows: the
//   queue is replaced wholesale on every `queue.updated`; requests open and
//   close by id.

import ATCAppServerAPI
import Foundation

struct ChatTranscript: Equatable {
    private(set) var items: [ThreadItem] = []
    private(set) var turns: [ThreadTurn] = []
    private(set) var requests: [ThreadRequest] = []
    private(set) var queue: [QueuedPrompt] = []
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
    }

    mutating func replaceRequests(_ requests: [ThreadRequest]) {
        self.requests = requests
    }

    mutating func replaceQueue(_ prompts: [QueuedPrompt]) {
        queue = prompts
    }

    /// Applies one stream event. Returns true when the copy is invalid and
    /// must be refetched (`snapshot.invalidated`).
    mutating func apply(_ event: ThreadEvent) -> Bool {
        switch event {
        case .item_started(let event):
            guard advance(to: event.seq) else { return false }
            upsert(event.item)
        case .item_updated(let event):
            guard advance(to: event.seq) else { return false }
            upsert(event.item)
        case .item_completed(let event):
            guard advance(to: event.seq) else { return false }
            upsert(event.item)
        case .turn_started(let event):
            guard advance(to: event.seq) else { return false }
            upsert(event.turn)
        case .turn_completed(let event):
            guard advance(to: event.seq) else { return false }
            upsert(event.turn)
        case .text_delta(let event):
            guard let index = items.firstIndex(where: { $0.id == event.itemId }) else { return false }
            items[index].appendText(event.delta)
        case .request_opened(let event):
            requests.removeAll { $0.id == event.request.id }
            requests.append(event.request)
        case .request_closed(let event):
            requests.removeAll { $0.id == event.requestId }
        case .queue_updated(let event):
            queue = event.prompts
        case .snapshot_invalidated:
            return true
        }
        return false
    }

    /// Moves the durable cursor forward; false when the change is already
    /// reflected and the event must be dropped.
    private mutating func advance(to eventSeq: Int) -> Bool {
        if let seq, eventSeq <= seq { return false }
        seq = eventSeq
        return true
    }

    private mutating func upsert(_ item: ThreadItem) {
        if let index = items.firstIndex(where: { $0.id == item.id }) {
            items[index] = item
            return
        }
        items.append(item)
    }

    private mutating func upsert(_ turn: ThreadTurn) {
        if let index = turns.firstIndex(where: { $0.id == turn.id }) {
            turns[index] = turn
            return
        }
        turns.append(turn)
    }
}

import ATCAppServerAPI
import Foundation
import Testing

@testable import ATC

/// The Chat reducer: the contract's merge rules for items, turns, deltas,
/// live state, and the seq cursor — pure, no server.
@Suite("ChatTranscript")
struct ChatTranscriptTests {
    private func loaded() -> ChatTranscript {
        var transcript = ChatTranscript()
        transcript.load(
            Fixtures.page(
                items: [Fixtures.userMessage("u1"), Fixtures.assistantText("a1", text: "Hel")],
                turns: [Fixtures.turn("turn1")],
                seq: 10,
                hasMore: true
            ))
        return transcript
    }

    @Test("load takes the page as-is: order, turns, seq, hasMore")
    func load() {
        let transcript = loaded()
        #expect(transcript.isLoaded)
        #expect(transcript.items.map(\.id) == ["u1", "a1"])
        #expect(transcript.seq == 10)
        #expect(transcript.hasMore)
        #expect(transcript.runningTurn?.id == "turn1")
    }

    @Test("items upsert by id in place; unknown ids append; turns likewise")
    func upsert() {
        var transcript = loaded()
        var needsReload = transcript.apply(
            .item_updated(
                .init(_type: .item_updated, seq: 11, item: Fixtures.assistantText("a1", text: "Hello", complete: true)))
        )
        #expect(!needsReload)
        needsReload = transcript.apply(
            .item_started(.init(_type: .item_started, seq: 12, item: Fixtures.command("c1"))))
        #expect(!needsReload)
        _ = transcript.apply(
            .turn_completed(.init(_type: .turn_completed, seq: 13, turn: Fixtures.turn("turn1", status: .completed))))

        #expect(transcript.items.map(\.id) == ["u1", "a1", "c1"])
        guard case .assistantText(let text) = transcript.items[1] else {
            Issue.record("expected assistant text")
            return
        }
        #expect(text.text == "Hello")
        #expect(text.complete)
        #expect(transcript.seq == 13)
        #expect(transcript.runningTurn == nil)
        #expect(transcript.turn(id: "turn1")?.status == .completed)
    }

    @Test("a durable event at or below the cursor is already reflected and dropped")
    func staleDurableEventsDropped() {
        var transcript = loaded()
        _ = transcript.apply(
            .item_updated(.init(_type: .item_updated, seq: 10, item: Fixtures.assistantText("a1", text: "OLD"))))
        _ = transcript.apply(
            .item_updated(.init(_type: .item_updated, seq: 3, item: Fixtures.assistantText("a1", text: "OLDER"))))
        guard case .assistantText(let text) = transcript.items[1] else {
            Issue.record("expected assistant text")
            return
        }
        #expect(text.text == "Hel")
        #expect(transcript.seq == 10)
    }

    @Test("text.delta extends the named streaming item and nothing else")
    func textDelta() {
        var transcript = loaded()
        _ = transcript.apply(.text_delta(.init(_type: .text_delta, itemId: "a1", delta: "lo")))
        _ = transcript.apply(.text_delta(.init(_type: .text_delta, itemId: "ghost", delta: "!")))
        _ = transcript.apply(.text_delta(.init(_type: .text_delta, itemId: "u1", delta: "!")))
        // A delta replayed behind the page that completed its item is
        // already in the text (deltas are never persisted; the completed
        // item carries the whole text).
        _ = transcript.apply(
            .item_completed(
                .init(
                    _type: .item_completed, seq: 11, item: Fixtures.assistantText("a1", text: "Hello", complete: true)))
        )
        _ = transcript.apply(.text_delta(.init(_type: .text_delta, itemId: "a1", delta: "Hello")))
        guard case .assistantText(let text) = transcript.items[1],
            case .userMessage(let user) = transcript.items[0]
        else {
            Issue.record("expected the loaded items")
            return
        }
        #expect(text.text == "Hello")
        #expect(user.text == "hi")
        #expect(transcript.items.count == 2)
        #expect(transcript.seq == 11)
    }

    @Test("snapshot.invalidated asks for a reload and leaves the cursor where the copy is")
    func snapshotInvalidated() {
        var transcript = loaded()
        let invalidated = transcript.apply(
            .snapshot_invalidated(.init(_type: .snapshot_invalidated, seq: 14, snapshotVersion: 2)))
        #expect(invalidated)
        #expect(transcript.seq == 10)
        #expect(transcript.snapshotVersion == 1)
    }

    @Test("an older page from another copy is discarded")
    func olderPageFromOtherCopy() {
        var transcript = loaded()
        transcript.prependOlder(
            Fixtures.page(items: [Fixtures.userMessage("u0")], seq: 10, snapshotVersion: 2, hasMore: false))
        #expect(transcript.items.map(\.id) == ["u1", "a1"])
        #expect(transcript.hasMore)
    }

    @Test("requests open and close by id; the queue is replaced wholesale")
    func liveState() {
        var transcript = loaded()
        _ = transcript.apply(.request_opened(.init(_type: .request_opened, request: Fixtures.approval("r1"))))
        _ = transcript.apply(.request_opened(.init(_type: .request_opened, request: Fixtures.approval("r2"))))
        _ = transcript.apply(.request_opened(.init(_type: .request_opened, request: Fixtures.approval("r1"))))
        #expect(transcript.requests.map(\.id) == ["r2", "r1"])
        _ = transcript.apply(.request_closed(.init(_type: .request_closed, requestId: "r2")))
        #expect(transcript.requests.map(\.id) == ["r1"])

        _ = transcript.apply(
            .queue_updated(.init(_type: .queue_updated, prompts: [Fixtures.queued("p1"), Fixtures.queued("p2")])))
        _ = transcript.apply(.queue_updated(.init(_type: .queue_updated, prompts: [Fixtures.queued("p2")])))
        #expect(transcript.queue.map(\.id) == ["p2"])
    }

    @Test("an older page prepends without duplicating and takes over hasMore")
    func prependOlder() {
        var transcript = loaded()
        transcript.prependOlder(
            Fixtures.page(
                items: [
                    Fixtures.userMessage("u-1", turn: "turn-1"), Fixtures.userMessage("u0", turn: "turn0"),
                    Fixtures.userMessage("u1"),
                ],
                turns: [Fixtures.turn("turn-1", status: .completed), Fixtures.turn("turn0", status: .completed)],
                seq: 10,
                hasMore: false
            ))
        #expect(transcript.items.map(\.id) == ["u-1", "u0", "u1", "a1"])
        #expect(transcript.turns.map(\.id) == ["turn-1", "turn0", "turn1"])
        #expect(!transcript.hasMore)
    }

    @Test("rows nest children under their parent to any depth and mark abnormal turn ends after their last item")
    func rows() {
        var transcript = ChatTranscript()
        transcript.load(
            Fixtures.page(
                items: [
                    Fixtures.userMessage("u1", turn: "t1"),
                    Fixtures.command("c1", turn: "t1"),
                    Fixtures.command("c2", turn: "t1", parent: "c1"),
                    Fixtures.command("c3", turn: "t1", parent: "c2"),
                    Fixtures.command("orphan", turn: "t1", parent: "older-page"),
                    Fixtures.userMessage("u2", turn: "t2"),
                ],
                turns: [Fixtures.turn("t1", status: .failed, error: "boom"), Fixtures.turn("t2")],
                seq: 5
            ))
        let rows = transcript.rows
        #expect(rows.map(\.id) == ["item:u1", "item:c1", "item:orphan", "turn:t1", "item:u2"])
        guard case .item(let node) = rows[1] else {
            Issue.record("expected the command row")
            return
        }
        #expect(node.children.map(\.id) == ["c2"])
        #expect(node.children.first?.children.map(\.id) == ["c3"])
    }
}

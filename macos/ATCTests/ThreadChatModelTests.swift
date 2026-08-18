import ATCAppServerAPI
import Foundation
import Testing

@testable import ATC

/// The Chat model's I/O around the reducer: load-on-connect, the resume
/// cursor, snapshot invalidation, live-state refetch, the runtime's
/// acquire/release ownership, and the composer-facing actions.
@MainActor
@Suite("ThreadChatModel")
struct ThreadChatModelTests {
    private struct Harness {
        let test: TestModel
        let streams: ScriptedThreadEventStream
        let chat: ThreadChatModel
    }

    private func acquired(
        _ client: ScriptableAppServerClient = ScriptableAppServerClient(),
        connect: Bool = true
    ) async throws -> Harness {
        Fixtures.seed(client)
        client.transcripts["thr1"] = Fixtures.page(
            items: [Fixtures.userMessage("u1"), Fixtures.assistantText("a1", text: "Hel")],
            turns: [Fixtures.turn("turn1")],
            seq: 10
        )
        client.threadRequests["thr1"] = [Fixtures.approval("r1")]
        client.threadQueues["thr1"] = [Fixtures.queued("p1")]
        let streams = ScriptedThreadEventStream()
        let test = try await makeModel(client: client, threadEvents: streams)
        let chat = test.runtime.acquireChat(threadID: "thr1")
        if connect {
            streams.connect("thr1")
            await settle(until: { chat.transcript.isLoaded && !chat.transcript.requests.isEmpty })
        }
        return Harness(test: test, streams: streams, chat: chat)
    }

    @Test("connect loads the newest page, then the live-only state; the cursor is the page's seq")
    func connectLoads() async throws {
        let harness = try await acquired()
        let chat = harness.chat

        #expect(chat.connection == .live)
        #expect(chat.transcript.items.map(\.id) == ["u1", "a1"])
        #expect(chat.transcript.requests.map(\.id) == ["r1"])
        #expect(chat.transcript.queue.map(\.id) == ["p1"])
        #expect(harness.test.client.transcriptReads.map(\.before) == [nil])
        let subscription = try #require(harness.streams.subscriptions.first)
        #expect(subscription.threadID == "thr1")
        #expect(subscription.after() == 10)
    }

    @Test("live events move the cursor; a reconnect refetches only requests and queue, never the transcript")
    func reconnectResumes() async throws {
        let harness = try await acquired()
        let chat = harness.chat
        let client = harness.test.client

        harness.streams.send("thr1", .text_delta(.init(_type: .text_delta, itemId: "a1", delta: "lo")))
        harness.streams.send(
            "thr1",
            .item_completed(
                .init(
                    _type: .item_completed, seq: 11, item: Fixtures.assistantText("a1", text: "Hello!", complete: true))
            ))
        await settle(until: { chat.transcript.seq == 11 })
        #expect(harness.streams.subscriptions[0].after() == 11)

        harness.streams.disconnect("thr1")
        await settle(until: { chat.connection == .reconnecting })
        // The transcript stays on screen through the outage.
        #expect(chat.transcript.items.count == 2)

        client.threadRequests["thr1"] = []
        harness.streams.connect("thr1")
        await settle(until: { chat.transcript.requests.isEmpty })
        #expect(chat.connection == .live)
        #expect(client.transcriptReads.count == 1)
    }

    @Test("snapshot.invalidated refetches the transcript in place and drops what the new page already holds")
    func snapshotInvalidated() async throws {
        let harness = try await acquired()
        let chat = harness.chat
        let client = harness.test.client

        client.transcripts["thr1"] = Fixtures.page(
            items: [Fixtures.userMessage("u1", text: "hi (re-read)")], turns: [], seq: 20, snapshotVersion: 2)
        harness.streams.send(
            "thr1", .snapshot_invalidated(.init(_type: .snapshot_invalidated, seq: 15, snapshotVersion: 2)))
        await settle(until: { chat.transcript.seq == 20 })

        #expect(client.transcriptReads.count == 2)
        #expect(chat.transcript.items.count == 1)
        // A change the re-read already contains is not applied twice.
        harness.streams.send(
            "thr1", .item_started(.init(_type: .item_started, seq: 18, item: Fixtures.command("stale"))))
        harness.streams.send(
            "thr1", .item_started(.init(_type: .item_started, seq: 21, item: Fixtures.command("fresh"))))
        await settle(until: { chat.transcript.seq == 21 })
        #expect(chat.transcript.items.map(\.id) == ["u1", "fresh"])
        #expect(harness.streams.subscriptions[0].after() == 21)
    }

    @Test("a reconnect that raced the first read replays nothing, so its connect reloads the copy")
    func liveOnlyResubscribeReloads() async throws {
        let client = ScriptableAppServerClient()
        let harness = try await acquired(client, connect: false)
        let chat = harness.chat
        client.delay = .milliseconds(50)
        harness.streams.connect("thr1")
        // The stream drops and comes back while the first read is in flight:
        // that subscription asked for no `after`, so nothing between the read
        // and the resubscribe would ever arrive — the copy must be reread.
        harness.streams.disconnect("thr1")
        harness.streams.connect("thr1")
        await settle(until: { client.transcriptReads.count == 2 })
        client.delay = nil
        await settle(until: { chat.transcript.isLoaded && chat.connection == .live })
        #expect(harness.streams.subscriptions[0].after() == 10)

        // A reconnect that did ask with the cursor replays instead.
        harness.streams.disconnect("thr1")
        harness.streams.connect("thr1")
        await settle(until: { chat.connection == .live })
        await drainPendingTasks()
        #expect(client.transcriptReads.count == 2)
    }

    @Test("a failed first load stays empty with an error, and the next connect retries it")
    func failedLoadRetries() async throws {
        let client = ScriptableAppServerClient()
        let harness = try await acquired(client, connect: false)
        let chat = harness.chat
        client.shouldFail = true
        harness.streams.connect("thr1")
        await settle(until: { chat.loadError != nil })
        #expect(!chat.transcript.isLoaded)
        #expect(harness.streams.subscriptions[0].after() == nil)

        client.shouldFail = false
        harness.streams.disconnect("thr1")
        harness.streams.connect("thr1")
        await settle(until: { chat.transcript.isLoaded })
        #expect(chat.loadError == nil)
        #expect(chat.transcript.items.count == 2)
    }

    @Test("load earlier pages before the oldest item and prepends it")
    func loadOlder() async throws {
        let client = ScriptableAppServerClient()
        Fixtures.seed(client)
        client.transcripts["thr1"] = Fixtures.page(items: [Fixtures.userMessage("u5")], seq: 10, hasMore: true)
        client.transcripts["thr1@before=u5"] = Fixtures.page(
            items: [Fixtures.userMessage("u4")], seq: 10, hasMore: false)
        let streams = ScriptedThreadEventStream()
        let test = try await makeModel(client: client, threadEvents: streams)
        let chat = test.runtime.acquireChat(threadID: "thr1")
        streams.connect("thr1")
        await settle(until: { chat.transcript.isLoaded })

        try await chat.loadOlder()

        #expect(chat.transcript.items.map(\.id) == ["u4", "u5"])
        #expect(!chat.transcript.hasMore)
        #expect(client.transcriptReads.map(\.before) == [nil, "u5"])
    }

    @Test("send goes to the server and reports refusal without touching the copy")
    func send() async throws {
        let harness = try await acquired()
        let chat = harness.chat
        let client = harness.test.client

        #expect(await chat.send("do it"))
        #expect(client.prompts.map(\.prompt) == ["do it"])
        #expect(chat.promptError == nil)

        client.promptThreadFailure = .init(
            _tag: .providerUnavailable, agentId: "codex", reason: "not-installed", message: "Codex is not installed")
        #expect(await chat.send("again") == false)
        #expect(chat.promptError == "Codex is not installed")
        #expect(chat.transcript.items.count == 2)
    }

    @Test("stop, answer, and withdraw reach the server as the contract's operations")
    func actions() async throws {
        let harness = try await acquired()
        let chat = harness.chat
        let client = harness.test.client

        try await chat.interrupt()
        try await chat.answer(requestID: "r1", .approval(.init(kind: .approval, decision: .acceptForSession)))
        try await chat.withdraw(promptID: "p1")

        #expect(client.interruptCount == 1)
        #expect(client.answers.map(\.requestID) == ["r1"])
        guard case .approval(let approval) = client.answers[0].answer else {
            Issue.record("expected an approval answer")
            return
        }
        #expect(approval.decision == .acceptForSession)
        #expect(client.withdrawnPrompts.map(\.promptID) == ["p1"])
        // The card closes only when the stream says so.
        #expect(chat.transcript.requests.map(\.id) == ["r1"])
        harness.streams.send("thr1", .request_closed(.init(_type: .request_closed, requestId: "r1")))
        await settle(until: { chat.transcript.requests.isEmpty })

        // A refused action throws the server's message.
        await #expect(throws: ServerError.self) {
            try await chat.withdraw(promptID: "gone")
        }
    }

    @Test("a failed resync keeps the cursor and holds durable events until a page lands")
    func failedResyncHoldsCursor() async throws {
        let harness = try await acquired()
        let chat = harness.chat
        let client = harness.test.client

        client.shouldFail = true
        harness.streams.send(
            "thr1", .snapshot_invalidated(.init(_type: .snapshot_invalidated, seq: 15, snapshotVersion: 2)))
        await settle(until: { chat.loadError != nil })
        // Neither the invalidation nor a later durable change moves the
        // reconnect cursor: a reconnect must be told about the re-read again.
        harness.streams.send("thr1", .item_started(.init(_type: .item_started, seq: 16, item: Fixtures.command("c16"))))
        await drainPendingTasks()
        #expect(harness.streams.subscriptions[0].after() == 10)
        #expect(chat.transcript.items.map(\.id) == ["u1", "a1"])
        #expect(client.transcriptReads.count == 3)

        // The next durable event retries the read; once it lands the copy
        // and cursor are the page's.
        client.shouldFail = false
        client.transcripts["thr1"] = Fixtures.page(items: [Fixtures.userMessage("u1")], seq: 20, snapshotVersion: 2)
        harness.streams.send("thr1", .item_started(.init(_type: .item_started, seq: 17, item: Fixtures.command("c17"))))
        await settle(until: { chat.transcript.seq == 20 })
        #expect(chat.loadError == nil)
        #expect(chat.transcript.items.map(\.id) == ["u1"])
        #expect(harness.streams.subscriptions[0].after() == 20)
    }

    @Test("an older page from a replaced copy is discarded")
    func olderPageFromReplacedCopyDiscarded() async throws {
        let client = ScriptableAppServerClient()
        Fixtures.seed(client)
        client.transcripts["thr1"] = Fixtures.page(
            items: [Fixtures.userMessage("u5")], seq: 10, snapshotVersion: 1, hasMore: true)
        client.transcripts["thr1@before=u5"] = Fixtures.page(
            items: [Fixtures.userMessage("u4")], seq: 10, snapshotVersion: 1, hasMore: false)
        let streams = ScriptedThreadEventStream()
        let test = try await makeModel(client: client, threadEvents: streams)
        let chat = test.runtime.acquireChat(threadID: "thr1")
        streams.connect("thr1")
        await settle(until: { chat.transcript.isLoaded })

        // The copy is replaced (snapshotVersion 2) before the older page,
        // read from version 1, comes back.
        client.transcripts["thr1"] = Fixtures.page(
            items: [Fixtures.userMessage("u9")], seq: 30, snapshotVersion: 2, hasMore: true)
        streams.send("thr1", .snapshot_invalidated(.init(_type: .snapshot_invalidated, seq: 25, snapshotVersion: 2)))
        await settle(until: { chat.transcript.seq == 30 })
        try await chat.loadOlder()

        #expect(chat.transcript.items.map(\.id) == ["u9"])
        #expect(chat.transcript.hasMore)
    }

    @Test("a failed live-state read is retried while the stream stays up")
    func liveStateRetries() async throws {
        let client = ScriptableAppServerClient()
        let harness = try await acquired(client, connect: false)
        let chat = harness.chat
        chat.liveStateRetryDelay = .milliseconds(1)
        client.threadRequests["thr1"] = []
        harness.streams.connect("thr1")
        await settle(until: { chat.transcript.isLoaded })
        // Requests were read empty; the queue read is what a later retry
        // will pick up. Make the next reads fail, then recover.
        client.shouldFail = true
        client.threadRequests["thr1"] = [Fixtures.approval("late")]
        harness.streams.disconnect("thr1")
        harness.streams.connect("thr1")
        await settle(until: { chat.connection == .live })
        await drainPendingTasks()
        #expect(chat.transcript.requests.isEmpty)

        client.shouldFail = false
        await settle(until: { chat.transcript.requests.map(\.id) == ["late"] })
    }

    @Test("the runtime shares one model across holds and stops it on the last release")
    func ownership() async throws {
        let harness = try await acquired()
        let runtime = harness.test.runtime

        let again = runtime.acquireChat(threadID: "thr1")
        #expect(again === harness.chat)
        #expect(harness.streams.subscriptions.count == 1)

        runtime.releaseChat(threadID: "thr1")
        #expect(runtime.acquireChat(threadID: "thr1") === harness.chat)
        runtime.releaseChat(threadID: "thr1")
        runtime.releaseChat(threadID: "thr1")

        // A fresh acquire is a fresh model with its own subscription.
        let fresh = runtime.acquireChat(threadID: "thr1")
        #expect(fresh !== harness.chat)
        #expect(harness.streams.subscriptions.count == 2)
        runtime.releaseChat(threadID: "thr1")
    }
}

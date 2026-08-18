import Foundation
import Testing

@testable import ATCAppServerTransport

private let eventsURL = URL(string: "http://127.0.0.1:7332/api/v1/threads/t1/events")!

private let itemStarted = """
    data: {"type":"item.started","seq":7,"item":{"type":"userMessage","id":"i1","turnId":"turn1","text":"hi"}}\n\n
    """
private let textDelta = """
    data: {"type":"text.delta","itemId":"i2","delta":"Hel"}\n\n
    """
private let snapshotInvalidated = """
    data: {"type":"snapshot.invalidated","seq":9,"snapshotVersion":2}\n\n
    """
// Exactly what the server's contract encoder emits for the request events.
private let questionOpened = """
    data: {"type":"request.opened","request":{"kind":"question","id":"req1","turnId":"t1","itemId":"i1","openedAt":"2026-08-18T10:00:00.000Z","questions":[{"id":"q1","header":"Pick","question":"Which?","options":[{"label":"A","description":"a"}],"multiSelect":false,"freeform":true,"secret":false}]}}\n\n
    """
private let approvalOpened = """
    data: {"type":"request.opened","request":{"kind":"approval","id":"req2","turnId":"t1","openedAt":"2026-08-18T10:00:00.000Z","title":"Run command?","reason":"because","subject":{"type":"command","command":"ls","cwd":"/tmp"}}}\n\n
    """
private let requestClosed = """
    data: {"type":"request.closed","requestId":"req1"}\n\n
    """
private let turnStarted = """
    data: {"type":"turn.started","seq":8,"turn":{"id":"turn1","status":"running","startedAt":"2026-08-18T10:00:00.000Z"}}\n\n
    """

/// A resume cursor the test moves as the consumer would.
private final class Cursor: @unchecked Sendable {
    private let lock = NSLock()
    private var value: Int?
    init(_ value: Int?) { self.value = value }
    var seq: Int? {
        get { lock.withLock { value } }
        set { lock.withLock { value = newValue } }
    }
}

@Suite("Thread event stream")
struct ThreadEventStreamTests {
    @Test("connected precedes events; durable and live-only events both decode")
    func connectAndDecode() async {
        let connector = ScriptedConnector([
            .stream([": connected\n\n", itemStarted, textDelta, snapshotInvalidated], thenHang: true)
        ])
        let stream = ThreadEventStream.stream(
            url: eventsURL, after: { nil }, configuration: fastConfiguration(), connector: connector.connect()
        )

        let events = await collect(stream, count: 4)

        guard events.count == 4 else {
            Issue.record("expected 4 events, got \(events)")
            return
        }
        #expect(events.first == .connected)
        guard case .event(.item_started(let started)) = events[1],
            case .userMessage(let message) = started.item
        else {
            Issue.record("expected item.started, got \(events[1])")
            return
        }
        #expect(started.seq == 7)
        #expect(message.text == "hi")
        guard case .event(.text_delta(let delta)) = events[2] else {
            Issue.record("expected text.delta, got \(events[2])")
            return
        }
        #expect(delta.itemId == "i2")
        #expect(delta.delta == "Hel")
        guard case .event(.snapshot_invalidated(let invalidated)) = events[3] else {
            Issue.record("expected snapshot.invalidated, got \(events[3])")
            return
        }
        #expect(invalidated.seq == 9)
        // No `after` → a live-only subscribe, no query at all.
        #expect(connector.urls == [eventsURL])
    }

    // Requests and turns carry `date-time` fields: they decode only through
    // the App Server's date transcoder, and a stream drops what it cannot
    // decode — so this is the test that keeps request cards appearing live.
    @Test("events with timestamps decode: a question, an approval, a close, a turn")
    func timestampedEventsDecode() async {
        let connector = ScriptedConnector([
            .stream(
                [": connected\n\n", questionOpened, approvalOpened, requestClosed, turnStarted], thenHang: true)
        ])
        let stream = ThreadEventStream.stream(
            url: eventsURL, after: { nil }, configuration: fastConfiguration(), connector: connector.connect()
        )

        let events = await collect(stream, count: 5)

        guard events.count == 5 else {
            Issue.record("expected 5 events, got \(events)")
            return
        }
        guard case .event(.request_opened(let opened)) = events[1], case .question(let question) = opened.request
        else {
            Issue.record("expected a question request.opened, got \(events[1])")
            return
        }
        #expect(question.id == "req1")
        #expect(question.questions.first?.options.first?.label == "A")
        guard case .event(.request_opened(let opened)) = events[2], case .approval(let approval) = opened.request
        else {
            Issue.record("expected an approval request.opened, got \(events[2])")
            return
        }
        #expect(approval.title == "Run command?")
        guard case .event(.request_closed(let closed)) = events[3] else {
            Issue.record("expected request.closed, got \(events[3])")
            return
        }
        #expect(closed.requestId == "req1")
        guard case .event(.turn_started(let started)) = events[4] else {
            Issue.record("expected turn.started, got \(events[4])")
            return
        }
        #expect(started.turn.startedAt != nil)
    }

    @Test("every (re)connect asks the caller for its resume seq")
    func resumesFromCursor() async {
        let cursor = Cursor(nil)
        let connector = ScriptedConnector([
            .stream([": connected\n\n", itemStarted], thenHang: true),
            .stream([": connected\n\n"], thenHang: true),
        ])
        let stream = ThreadEventStream.stream(
            url: eventsURL,
            after: { cursor.seq },
            configuration: fastConfiguration(),
            connector: connector.connect()
        )

        // The consumer records the seq it applied, then the server drops the
        // connection — in that order, as a real consumer would experience it.
        let events = await collect(stream, count: 4) { event in
            guard case .event(.item_started(let started)) = event else { return }
            cursor.seq = started.seq
            connector.endOpenConnection()
        }

        guard events.count == 4 else {
            Issue.record("expected 4 events, got \(events)")
            return
        }
        #expect(events[2] == .disconnected)
        #expect(events[3] == .connected)
        #expect(connector.urls.count == 2)
        #expect(connector.urls[0] == eventsURL)
        #expect(connector.urls[1].absoluteString == "\(eventsURL.absoluteString)?after=7")
    }

    @Test("a silent stream is torn down and reconnected")
    func silenceTimeout() async {
        let connector = ScriptedConnector([
            .stream([": connected\n\n"], thenHang: true),
            .stream([": connected\n\n"], thenHang: true),
        ])
        let stream = ThreadEventStream.stream(
            url: eventsURL,
            after: { 3 },
            configuration: fastConfiguration(silenceTimeout: .milliseconds(50)),
            connector: connector.connect()
        )

        let events = await collect(stream, count: 3)

        #expect(events == [.connected, .disconnected, .connected])
        #expect(connector.urls.allSatisfy { $0.query() == "after=3" })
    }

    @Test("failed connects retry silently until one opens, with headers intact")
    func failedConnectsStaySilent() async {
        let connector = ScriptedConnector([
            .rejected(404),
            .failure,
            .stream([": connected\n\n"], thenHang: true),
        ])
        let stream = ThreadEventStream.stream(
            url: eventsURL,
            after: { nil },
            headers: ["Authorization": "Bearer token-1"],
            configuration: fastConfiguration(),
            connector: connector.connect()
        )

        let events = await collect(stream, count: 1)

        #expect(events == [.connected])
        #expect(connector.attempts == 3)
        #expect(connector.headers == ["Authorization": "Bearer token-1"])
    }

    @Test("undecodable payloads are dropped, not fatal")
    func undecodablePayloadDropped() async {
        let connector = ScriptedConnector([
            .stream(
                [": connected\n\n", "data: {\"type\":\"galaxy.exploded\",\"seq\":1}\n\n", textDelta],
                thenHang: true
            )
        ])
        let stream = ThreadEventStream.stream(
            url: eventsURL, after: { nil }, configuration: fastConfiguration(), connector: connector.connect()
        )

        let events = await collect(stream, count: 2)

        guard events.count == 2 else {
            Issue.record("expected 2 events, got \(events)")
            return
        }
        #expect(events.first == .connected)
        guard case .event(.text_delta) = events[1] else {
            Issue.record("expected text.delta, got \(events[1])")
            return
        }
    }
}

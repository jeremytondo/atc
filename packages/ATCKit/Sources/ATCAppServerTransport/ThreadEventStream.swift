// ThreadEventStream: one thread's SSE event stream
// (`GET /api/v1/threads/:id/events`) as a long-lived AsyncStream.
//
// The engine (connect handshake, watchdog, backoff) is `SSEStream`; what is
// owned here is the contract of this particular stream:
// - Unlike the resource stream it resumes: durable events carry `seq`, and
//   the consumer tracks the highest one seen. `after` is asked on every
//   (re)connect, so a reconnect replays exactly the changes missed — the
//   consumer never refetches the transcript on `.disconnected`. (Live-only
//   events — text deltas, requests, the queue — are not replayed: `.connected`
//   is the cue to refetch the pending requests and the queue.)
// - A subscriber whose `after` predates a provider re-read is answered with
//   `snapshot.invalidated`; that is a normal event here. What to do about it
//   is the consumer's policy — the stream keeps running either way.
// - The generated client's `subscribeThreadEvents` returns a raw body; this
//   stream is the sanctioned way to consume it, as `ResourceEventStream` is
//   for `/events`.

import ATCAppServerAPI
import Foundation

public enum ThreadEventStream {
    public typealias ThreadEvent = Components.Schemas.ThreadEvent
    public typealias Configuration = EventStreamConfiguration
    typealias Connector = SSEStream.Connector

    public enum Event: Sendable, Equatable {
        /// Stream is open; durable changes after `after` are replaying, so
        /// refetch only the live-only state (requests, queue) now.
        case connected
        /// One thread event, durable or live-only.
        case event(ThreadEvent)
        /// Stream lost; a reconnect from the caller's `after` follows.
        case disconnected
    }

    /// - Parameter after: the highest durable `seq` the consumer has applied,
    ///   asked on every (re)connect; `nil` subscribes live-only.
    public static func live(
        baseURL: URL,
        threadId: String,
        headers: [String: String] = [:],
        configuration: Configuration = Configuration(),
        after: @escaping @Sendable () -> Int?
    ) -> AsyncStream<Event> {
        stream(
            url: baseURL.appending(components: "api", "v1", "threads", threadId, "events"),
            after: after,
            headers: headers,
            configuration: configuration,
            connector: SSEStream.liveConnector(configuration: configuration)
        )
    }

    static func stream(
        url: URL,
        after: @escaping @Sendable () -> Int?,
        headers: [String: String] = [:],
        configuration: Configuration = Configuration(),
        connector: @escaping Connector
    ) -> AsyncStream<Event> {
        SSEStream.stream(
            request: {
                guard let seq = after() else { return url }
                return url.appending(queryItems: [URLQueryItem(name: "after", value: String(seq))])
            },
            headers: headers,
            configuration: configuration,
            connector: connector
        ) { signal in
            switch signal {
            case .connected:
                return .connected
            case .disconnected:
                return .disconnected
            case .data(let payload):
                guard
                    let event = try? makeJSONDecoder().decode(ThreadEvent.self, from: Data(payload.utf8))
                else { return nil }
                return .event(event)
            }
        }
    }
}

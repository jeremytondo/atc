// ResourceEventStream: the App Server's SSE resource-change stream
// (`GET /api/v1/events`) as a long-lived AsyncStream.
//
// The engine (connect handshake, watchdog, backoff) is `SSEStream`; what is
// owned here is the contract of this particular stream:
// - Delivery is ephemeral with no replay: every reconnect emits `.connected`
//   again and the consumer must resync by refetching, never resume.
//   Refetching on `.connected` is race-free (see SSEStream).
// - Events are thin invalidations naming a resource; the consumer coalesces
//   and refetches what they name.

import ATCAppServerAPI
import Foundation

public enum ResourceEventStream {
    public typealias Change = Components.Schemas.ResourceChangedEvent
    public typealias Configuration = EventStreamConfiguration
    typealias Connector = SSEStream.Connector

    public enum Event: Sendable, Equatable {
        /// Stream is open and reconciled server-side: refetch everything now.
        case connected
        /// A named resource changed: coalesce and refetch what it names.
        case change(Change)
        /// Stream lost; a reconnect attempt follows automatically.
        case disconnected
    }

    public static func live(
        baseURL: URL,
        headers: [String: String] = [:],
        configuration: Configuration = Configuration()
    ) -> AsyncStream<Event> {
        stream(
            url: baseURL.appending(path: "api/v1/events"),
            headers: headers,
            configuration: configuration,
            connector: SSEStream.liveConnector(configuration: configuration)
        )
    }

    static func stream(
        url: URL,
        headers: [String: String] = [:],
        configuration: Configuration = Configuration(),
        connector: @escaping Connector
    ) -> AsyncStream<Event> {
        SSEStream.stream(
            request: { url },
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
                guard let change = try? makeJSONDecoder().decode(Change.self, from: Data(payload.utf8))
                else { return nil }
                return .change(change)
            }
        }
    }
}

// AttachProtocol: the pure, transport-free rules of the App Server's
// terminal-attach WebSocket — URL construction, dimension clamping, control
// frames, and close classification.
//
// The wire contract is owned by `packages/attach-protocol/README.md` and the
// server module `app-server/src/terminals/attachProtocol.ts`; the shared JSON
// vectors in `packages/attach-protocol/fixtures/` pin this implementation in
// tests so drift between clients fails a test, not a terminal window.

import Foundation

public enum AttachProtocol {
    /// Server defaults when no size accompanies an attach.
    public static let fallbackCols = 80
    public static let fallbackRows = 24

    /// `ws(s)://<host>/api/v1/terminals/{terminalId}/attach?cols=&rows=` —
    /// scheme derived from the HTTP base URL.
    public static func attachURL(
        base: URL,
        terminalID: String,
        size: (cols: Int, rows: Int)? = nil
    ) -> URL {
        var components = URLComponents(url: base, resolvingAgainstBaseURL: false) ?? URLComponents()
        components.scheme = components.scheme == "https" ? "wss" : "ws"
        // Absolute path, like the server authority's `new URL(path, base)`:
        // a trailing slash on a user-entered base URL must not double up.
        components.path = "/api/v1/terminals/\(terminalID)/attach"
        if let size {
            components.queryItems = [
                URLQueryItem(name: "cols", value: String(size.cols)),
                URLQueryItem(name: "rows", value: String(size.rows)),
            ]
        }
        // The contract's URLs (loopback origin + path segments) always
        // compose into a valid URL.
        return components.url!
    }

    /// The one dimension rule for cols/rows everywhere they occur: floored
    /// and clamped into [1, 1000]; anything non-finite takes the fallback
    /// (matching the server's `Number.isFinite` guard).
    public static func clampDimension(_ value: Double?, fallback: Int) -> Int {
        guard let value, value.isFinite else { return fallback }
        return Int(max(1, min(1000, value.rounded(.down))))
    }

    /// String variant for query parameters: malformed unless the entire
    /// string is numeric (a numeric prefix does not count).
    public static func clampDimension(_ raw: String?, fallback: Int) -> Int {
        clampDimension(raw.flatMap(Double.init), fallback: fallback)
    }

    /// Text-frame control vocabulary. Binary frames are raw terminal bytes
    /// and never appear here.
    public enum ControlFrame: Sendable, Equatable {
        case resize(cols: Int, rows: Int)
        case ping
        case pong

        public var wire: String {
            switch self {
            case .resize(let cols, let rows):
                return #"{"type":"resize","cols":\#(cols),"rows":\#(rows)}"#
            case .ping:
                return #"{"type":"ping"}"#
            case .pong:
                return #"{"type":"pong"}"#
            }
        }

        /// Returns nil for unknown or malformed frames — the protocol says
        /// they are ignored, never fatal. JSONDecoder gives strict number
        /// typing (booleans and numeric strings are malformed, as on the
        /// server), and `clampDimension` makes the Int conversion total, so
        /// no wire value can trap.
        public static func decode(_ text: String) -> ControlFrame? {
            guard let wire = try? JSONDecoder().decode(WireFrame.self, from: Data(text.utf8))
            else { return nil }
            switch wire.type {
            case "resize":
                guard let cols = wire.cols, let rows = wire.rows else { return nil }
                return .resize(
                    cols: clampDimension(cols, fallback: fallbackCols),
                    rows: clampDimension(rows, fallback: fallbackRows)
                )
            case "ping":
                return .ping
            case "pong":
                return .pong
            default:
                return nil
            }
        }

        private struct WireFrame: Decodable {
            let type: String
            let cols: Double?
            let rows: Double?
        }
    }

    /// Reason string the client sends on deliberate detach (close code 1000).
    public static let detachReason = "detach"

    /// What a close tells the client about the terminal.
    public enum CloseDisposition: Sendable, Equatable {
        /// `1000 terminal_ended` — authoritative: a complete multiplexer
        /// inventory proved the session gone; the tombstone is written.
        case terminalEnded
        /// `1000 detach` — deliberate client detach; the session runs on.
        case detach
        /// Everything else (`1011 attach_failed|zmx_unavailable|ping_timeout`,
        /// `1006`, unknown codes): says nothing about the terminal — refetch
        /// it and reattach if still live.
        case retryable
    }

    public static func classifyClose(code: Int, reason: String?) -> CloseDisposition {
        if code == 1000 && reason == "terminal_ended" { return .terminalEnded }
        if code == 1000 && reason == detachReason { return .detach }
        return .retryable
    }
}

import Foundation

/// Typed failures from the Cockpit API.
public enum CockpitError: Error, Sendable {
    /// The server returned a structured error envelope
    /// (`{"error":"code","message":"...","sessionId?":""}`).
    case api(code: String, message: String, sessionID: String?)
    /// Non-2xx response without a decodable error envelope.
    case badStatus(Int)
    /// URLSession-level failure (connection refused, timeout, …).
    case transport(underlying: any Error)
    /// The response body could not be decoded.
    case decoding(underlying: any Error)
}

extension CockpitError: LocalizedError {
    public var errorDescription: String? {
        switch self {
        case .api(_, let message, _):
            return message
        case .badStatus(let status):
            return "Server returned HTTP \(status)."
        case .transport(let underlying):
            return underlying.localizedDescription
        case .decoding:
            return "Could not decode the server response."
        }
    }

    /// The machine-readable error code, when the server sent one.
    public var apiCode: String? {
        if case .api(let code, _, _) = self { return code }
        return nil
    }
}

/// Wire format of Cockpit error responses.
struct ErrorEnvelope: Decodable {
    var error: String
    var message: String
    var sessionId: String?
}

import Foundation

/// An atc server endpoint: base URL plus optional bearer token.
/// Produces REST URLs, the attach WebSocket URL, and auth headers.
public struct ATCServer: Sendable, Hashable {
    public var baseURL: URL
    public var token: String?

    public init(baseURL: URL, token: String? = nil) {
        self.baseURL = baseURL
        self.token = (token?.isEmpty == true) ? nil : token
    }

    public var authHeaders: [String: String] {
        guard let token else { return [:] }
        return ["Authorization": "Bearer \(token)"]
    }

    public func restURL(_ path: String, query: [URLQueryItem] = []) -> URL {
        var url = baseURL.appending(path: "api").appending(path: path)
        if !query.isEmpty {
            url.append(queryItems: query)
        }
        return url
    }

    /// `ws(s)://…/api/sessions/{id}/attach`
    public func attachURL(sessionID: String) -> URL {
        let rest = restURL("sessions/\(sessionID)/attach")
        var components = URLComponents(url: rest, resolvingAgainstBaseURL: false)!
        components.scheme = (components.scheme == "https") ? "wss" : "ws"
        return components.url!
    }
}

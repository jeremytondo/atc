import Foundation

/// Body of `POST /api/sessions/start`.
public struct StartSessionRequest: Encodable, Sendable, Hashable {
    public var action: String
    public var environment: String?
    public var params: [String: JSONValue]?
    public var workingDir: String
    public var prompt: String?
    public var name: String?

    public init(
        action: String,
        environment: String? = nil,
        params: [String: JSONValue]? = nil,
        workingDir: String,
        prompt: String? = nil,
        name: String? = nil
    ) {
        self.action = action
        self.environment = environment
        self.params = params
        self.workingDir = workingDir
        self.prompt = prompt
        self.name = name
    }
}

/// `GET /api/health`.
public struct Health: Decodable, Sendable, Hashable {
    public var status: String

    public init(status: String) {
        self.status = status
    }
}

/// `GET /api/version`.
public struct Version: Decodable, Sendable, Hashable {
    public var name: String
    public var version: String
    public var commit: String

    public init(name: String, version: String, commit: String) {
        self.name = name
        self.version = version
        self.commit = commit
    }
}

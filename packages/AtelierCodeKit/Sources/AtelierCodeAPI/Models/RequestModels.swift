import Foundation

/// Body of `POST /api/sessions/start`. Exactly one of `workingDir` or
/// `projectId` is required — they are mutually exclusive, and the server
/// enforces the constraint. Nil fields are omitted by the synthesized
/// `Encodable`.
public struct StartSessionRequest: Encodable, Sendable, Hashable {
    public var action: String
    public var environment: String?
    public var params: [String: JSONValue]?
    public var workingDir: String?
    public var projectId: String?
    public var prompt: String?
    public var name: String?

    public init(
        action: String,
        environment: String? = nil,
        params: [String: JSONValue]? = nil,
        workingDir: String? = nil,
        projectId: String? = nil,
        prompt: String? = nil,
        name: String? = nil
    ) {
        self.action = action
        self.environment = environment
        self.params = params
        self.workingDir = workingDir
        self.projectId = projectId
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

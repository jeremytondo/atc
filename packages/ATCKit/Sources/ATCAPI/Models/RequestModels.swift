import Foundation

/// Body of `POST /api/sessions/start`. `workspaceId` is required; the
/// session launches in the workspace's project working directory. A nil
/// `action` launches the Interactive Shell, which accepts neither `params`
/// nor `prompt`. Nil fields are omitted by the synthesized `Encodable`.
public struct StartSessionRequest: Encodable, Sendable, Hashable {
    public var workspaceId: String
    public var action: String?
    public var environment: String?
    public var params: [String: JSONValue]?
    public var prompt: String?
    public var name: String?

    public init(
        workspaceId: String,
        action: String? = nil,
        environment: String? = nil,
        params: [String: JSONValue]? = nil,
        prompt: String? = nil,
        name: String? = nil
    ) {
        self.workspaceId = workspaceId
        self.action = action
        self.environment = environment
        self.params = params
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

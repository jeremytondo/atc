import Foundation

/// Body of `POST /api/sessions/start`. A nil `actionId` launches the
/// Interactive Shell. Nil fields are omitted by the synthesized `Encodable`.
public struct StartSessionRequest: Encodable, Sendable, Hashable {
    public var workspaceId: String
    public var actionId: String?
    public var name: String?

    public init(
        workspaceId: String,
        actionId: String? = nil,
        name: String? = nil
    ) {
        self.workspaceId = workspaceId
        self.actionId = actionId
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

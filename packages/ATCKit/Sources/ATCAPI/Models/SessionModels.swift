import Foundation

public enum SessionStatus: String, Codable, Sendable, CaseIterable, Hashable {
    case live
    case ended
}

/// Workspace reference nested on sessions.
public struct SessionWorkspace: Codable, Sendable, Hashable, Identifiable {
    public var id: String
    public var name: String

    public init(id: String, name: String) {
        self.id = id
        self.name = name
    }
}

/// Derived project reference nested on sessions, reached through the
/// session's workspace.
public struct SessionProject: Codable, Sendable, Hashable, Identifiable {
    public var id: String
    public var name: String

    public init(id: String, name: String) {
        self.id = id
        self.name = name
    }
}

/// The shared shape returned by session list, detail, start, and rename APIs.
public struct Session: Codable, Sendable, Hashable, Identifiable {
    public var id: String
    /// Workspace-local human-facing address. Nil when the server predates
    /// session indexes.
    public var sessionIndex: Int?
    public var name: String?
    /// Copied launch identity. Both fields are nil for the Interactive Shell.
    public var actionId: String?
    public var actionName: String?
    public var isAgent: Bool
    public var workingDir: String
    public var status: SessionStatus
    public var createdAt: Date
    public var updatedAt: Date
    public var workspace: SessionWorkspace?
    public var project: SessionProject?

    public init(
        id: String,
        sessionIndex: Int? = nil,
        name: String? = nil,
        actionId: String? = nil,
        actionName: String? = nil,
        isAgent: Bool,
        workingDir: String,
        status: SessionStatus,
        createdAt: Date,
        updatedAt: Date,
        workspace: SessionWorkspace? = nil,
        project: SessionProject? = nil
    ) {
        self.id = id
        self.sessionIndex = sessionIndex
        self.name = name
        self.actionId = actionId
        self.actionName = actionName
        self.isAgent = isAgent
        self.workingDir = workingDir
        self.status = status
        self.createdAt = createdAt
        self.updatedAt = updatedAt
        self.workspace = workspace
        self.project = project
    }

    /// Human label for what the session launched.
    public var actionLabel: String { actionName ?? "Shell" }

    /// Best display name: user-given name, else what was launched.
    public var displayName: String {
        if let name, !name.isEmpty { return name }
        return actionLabel
    }
}

struct SessionsEnvelope: Decodable {
    var sessions: [Session]
}

import Foundation

public enum SessionStatus: String, Codable, Sendable, CaseIterable, Hashable {
    case starting
    case running
    case failed
    case terminated
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
/// session's workspace — kept so clients that group by project keep working.
public struct SessionProject: Codable, Sendable, Hashable, Identifiable {
    public var id: String
    public var name: String

    public init(id: String, name: String) {
        self.id = id
        self.name = name
    }
}

/// One entry from `GET /api/sessions` (wrapped in `{"sessions":[...]}`).
public struct Session: Codable, Sendable, Hashable, Identifiable {
    public var id: String
    public var name: String?
    /// The launch action; nil means the Interactive Shell.
    public var action: String?
    public var environment: String
    public var workingDir: String
    public var status: SessionStatus
    public var attachable: Bool
    public var failureReason: String?
    public var failureCode: String?
    public var createdAt: Date
    public var updatedAt: Date
    public var terminatedAt: Date?
    public var archivedAt: Date?
    /// The workspace this session belongs to.
    public var workspace: SessionWorkspace?
    /// The workspace's project, derived server-side.
    public var project: SessionProject?

    public init(
        id: String,
        name: String? = nil,
        action: String? = nil,
        environment: String,
        workingDir: String,
        status: SessionStatus,
        attachable: Bool,
        failureReason: String? = nil,
        failureCode: String? = nil,
        createdAt: Date,
        updatedAt: Date,
        terminatedAt: Date? = nil,
        archivedAt: Date? = nil,
        workspace: SessionWorkspace? = nil,
        project: SessionProject? = nil
    ) {
        self.id = id
        self.name = name
        self.action = action
        self.environment = environment
        self.workingDir = workingDir
        self.status = status
        self.attachable = attachable
        self.failureReason = failureReason
        self.failureCode = failureCode
        self.createdAt = createdAt
        self.updatedAt = updatedAt
        self.terminatedAt = terminatedAt
        self.archivedAt = archivedAt
        self.workspace = workspace
        self.project = project
    }

    /// Archived is not a status — it's a non-null `archivedAt`.
    public var isArchived: Bool { archivedAt != nil }

    /// Human label for what the session launched: the action name, or
    /// "Shell" for the Interactive Shell.
    public var actionLabel: String { action ?? "Shell" }

    /// Best display name: user-given name, else what was launched.
    public var displayName: String {
        if let name, !name.isEmpty { return name }
        return actionLabel
    }
}

/// `GET /api/sessions/{id}` and the response of start/terminate/archive/
/// unarchive.
public struct SessionDetail: Codable, Sendable, Hashable, Identifiable {
    public var id: String
    public var name: String?
    /// The launch action; nil means the Interactive Shell.
    public var action: String?
    public var environment: String
    public var params: [String: JSONValue]?
    public var workingDir: String
    public var prompt: String?
    public var status: SessionStatus
    public var attachable: Bool
    public var failureReason: String?
    public var failureCode: String?
    public var createdAt: Date
    public var updatedAt: Date
    public var terminatedAt: Date?
    public var archivedAt: Date?
    /// The workspace this session belongs to.
    public var workspace: SessionWorkspace?
    /// The workspace's project, derived server-side.
    public var project: SessionProject?

    public var isArchived: Bool { archivedAt != nil }

    /// The list-shaped projection of this detail, for merging into a
    /// sessions array after a mutation.
    public var asSession: Session {
        Session(
            id: id,
            name: name,
            action: action,
            environment: environment,
            workingDir: workingDir,
            status: status,
            attachable: attachable,
            failureReason: failureReason,
            failureCode: failureCode,
            createdAt: createdAt,
            updatedAt: updatedAt,
            terminatedAt: terminatedAt,
            archivedAt: archivedAt,
            workspace: workspace,
            project: project
        )
    }
}

struct SessionsEnvelope: Decodable {
    var sessions: [Session]
}

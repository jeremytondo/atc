import Foundation

/// A Cockpit project: a named, persistent working directory that sessions
/// can be scoped to. Maps to the Project JSON returned by
/// `GET/POST/PATCH /api/projects` and the archive/unarchive endpoints.
public struct Project: Codable, Sendable, Hashable, Identifiable {
    public var id: String
    public var name: String
    public var workingDir: String
    public var createdAt: Date
    public var updatedAt: Date
    /// Non-null once the project is archived; omitted while active.
    public var archivedAt: Date?

    public init(
        id: String,
        name: String,
        workingDir: String,
        createdAt: Date,
        updatedAt: Date,
        archivedAt: Date? = nil
    ) {
        self.id = id
        self.name = name
        self.workingDir = workingDir
        self.createdAt = createdAt
        self.updatedAt = updatedAt
        self.archivedAt = archivedAt
    }

    /// Archived is not a status — it's a non-null `archivedAt`.
    public var isArchived: Bool { archivedAt != nil }
}

/// `GET /api/projects` and `GET /api/projects/{id}/sessions`'s project list
/// wrapper (`{"projects":[...]}`, newest first).
struct ProjectsEnvelope: Decodable {
    var projects: [Project]
}

import Foundation

/// An atc workspace: a named unit of work inside a project that groups
/// sessions. Maps to the Workspace JSON returned by
/// `GET/POST/PATCH /api/workspaces` and the archive/unarchive endpoints.
/// Deleting a workspace removes session metadata only — files are never
/// touched.
public struct Workspace: Codable, Sendable, Hashable, Identifiable {
    public var id: String
    public var projectId: String
    public var name: String
    public var createdAt: Date
    public var updatedAt: Date
    /// Non-null once the workspace is archived; omitted while active.
    public var archivedAt: Date?

    public init(
        id: String,
        projectId: String,
        name: String,
        createdAt: Date,
        updatedAt: Date,
        archivedAt: Date? = nil
    ) {
        self.id = id
        self.projectId = projectId
        self.name = name
        self.createdAt = createdAt
        self.updatedAt = updatedAt
        self.archivedAt = archivedAt
    }

    /// Archived is not a status — it's a non-null `archivedAt`.
    public var isArchived: Bool { archivedAt != nil }
}

/// `GET /api/workspaces` and `GET /api/workspaces/{id}/sessions`'s workspace
/// list wrapper (`{"workspaces":[...]}`, newest first).
struct WorkspacesEnvelope: Decodable {
    var workspaces: [Workspace]
}

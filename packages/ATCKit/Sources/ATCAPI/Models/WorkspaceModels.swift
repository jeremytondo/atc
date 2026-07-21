import Foundation

/// An atc workspace: a named unit of work inside a project that groups
/// sessions. Maps to the Workspace JSON returned by
/// `GET/POST/PATCH /api/workspaces`.
/// Deleting a workspace removes session metadata only — files are never
/// touched.
public struct Workspace: Codable, Sendable, Hashable, Identifiable {
    public var id: String
    public var projectId: String
    public var name: String
    public var createdAt: Date
    public var updatedAt: Date

    public init(
        id: String,
        projectId: String,
        name: String,
        createdAt: Date,
        updatedAt: Date
    ) {
        self.id = id
        self.projectId = projectId
        self.name = name
        self.createdAt = createdAt
        self.updatedAt = updatedAt
    }
}

/// `GET /api/workspaces` and `GET /api/workspaces/{id}/sessions`'s workspace
/// list wrapper (`{"workspaces":[...]}`, newest first).
struct WorkspacesEnvelope: Decodable {
    var workspaces: [Workspace]
}

import Foundation

/// An atc project: a named, persistent working directory that contains
/// workspaces. Maps to the Project JSON returned by `GET/POST/PATCH /api/projects`.
public struct Project: Codable, Sendable, Hashable, Identifiable {
    public var id: String
    public var name: String
    public var workingDir: String
    public var createdAt: Date
    public var updatedAt: Date

    public init(
        id: String,
        name: String,
        workingDir: String,
        createdAt: Date,
        updatedAt: Date
    ) {
        self.id = id
        self.name = name
        self.workingDir = workingDir
        self.createdAt = createdAt
        self.updatedAt = updatedAt
    }
}

/// `GET /api/projects` and `GET /api/projects/{id}/sessions`'s project list
/// wrapper (`{"projects":[...]}`, newest first).
struct ProjectsEnvelope: Decodable {
    var projects: [Project]
}

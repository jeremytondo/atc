import Foundation

/// One entry from `GET /api/fs/roots` (wrapped in `{"roots":[...]}`).
/// `path` is the expanded, cleaned, absolute path on the workstation.
public struct RemoteWorkspaceRoot: Codable, Sendable, Hashable, Identifiable {
    public var id: String { path }
    public let label: String
    public let path: String

    public init(label: String, path: String) {
        self.label = label
        self.path = path
    }
}

/// Classification of a directory entry. Symlinks are classified by their
/// target; broken symlinks, sockets, FIFOs, and devices are `.unknown`
/// (visible, not enterable).
public enum RemoteEntryKind: String, Codable, Sendable {
    case directory
    case file
    case unknown

    /// Unrecognized raw values decode as `.unknown` so a future server
    /// adding kinds doesn't break old clients.
    public init(from decoder: any Decoder) throws {
        let raw = try decoder.singleValueContainer().decode(String.self)
        self = RemoteEntryKind(rawValue: raw) ?? .unknown
    }
}

/// One child of a listed directory. `path` is lexical
/// (symlink-preserving) and is the entry's identity — there are no IDs.
public struct RemoteEntry: Codable, Sendable, Hashable, Identifiable {
    public var id: String { path }
    public let name: String
    public let path: String
    public let kind: RemoteEntryKind
    public let isSymlink: Bool
    /// Bytes; present only for `kind == .file`.
    public let size: Int64?
    /// Present for `.file` and `.directory`, absent for `.unknown`.
    public let modifiedAt: Date?

    public init(
        name: String,
        path: String,
        kind: RemoteEntryKind,
        isSymlink: Bool,
        size: Int64? = nil,
        modifiedAt: Date? = nil
    ) {
        self.name = name
        self.path = path
        self.kind = kind
        self.isSymlink = isSymlink
        self.size = size
        self.modifiedAt = modifiedAt
    }
}

/// Response of `GET /api/fs/list`. `path` is the cleaned lexical path that
/// was listed — the client's canonical current-directory state. Entries
/// arrive pre-sorted (directories first, dotfiles first within group,
/// case-insensitive) and pre-filtered per `showHidden`.
public struct DirectoryListing: Codable, Sendable, Hashable {
    public let path: String
    public let truncated: Bool
    public let entries: [RemoteEntry]

    public init(path: String, truncated: Bool, entries: [RemoteEntry]) {
        self.path = path
        self.truncated = truncated
        self.entries = entries
    }
}

struct RootsEnvelope: Decodable {
    var roots: [RemoteWorkspaceRoot]
}

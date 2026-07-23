import Foundation

/// One complete action definition returned by the Actions API.
public struct ATCAction: Codable, Sendable, Hashable, Identifiable {
    public var id: String
    public var name: String
    public var description: String?
    public var enabled: Bool
    public var command: String
    public var args: [String]
    public var isAgent: Bool

    public init(
        id: String,
        name: String,
        description: String? = nil,
        enabled: Bool,
        command: String,
        args: [String],
        isAgent: Bool
    ) {
        self.id = id
        self.name = name
        self.description = description
        self.enabled = enabled
        self.command = command
        self.args = args
        self.isAgent = isAgent
    }
}

/// Body for `POST /api/actions`. Optional fields are omitted so the server
/// can apply its defaults.
public struct ActionCreate: Encodable, Sendable, Hashable {
    public var name: String
    public var description: String?
    public var command: String
    public var args: [String]?
    public var enabled: Bool?
    public var isAgent: Bool?

    public init(
        name: String,
        description: String? = nil,
        command: String,
        args: [String]? = nil,
        enabled: Bool? = nil,
        isAgent: Bool? = nil
    ) {
        self.name = name
        self.description = description
        self.command = command
        self.args = args
        self.enabled = enabled
        self.isAgent = isAgent
    }
}

/// Body for `PATCH /api/actions/{id}`. Nil properties are omitted.
/// Set `clearDescription` to encode an explicit null description.
public struct ActionPatch: Encodable, Sendable, Hashable {
    public var name: String?
    public var description: String?
    public var clearDescription: Bool
    public var command: String?
    public var args: [String]?
    public var enabled: Bool?
    public var isAgent: Bool?

    public init(
        name: String? = nil,
        description: String? = nil,
        clearDescription: Bool = false,
        command: String? = nil,
        args: [String]? = nil,
        enabled: Bool? = nil,
        isAgent: Bool? = nil
    ) {
        self.name = name
        self.description = description
        self.clearDescription = clearDescription
        self.command = command
        self.args = args
        self.enabled = enabled
        self.isAgent = isAgent
    }

    private enum CodingKeys: String, CodingKey {
        case name, description, command, args, enabled, isAgent
    }

    public func encode(to encoder: any Encoder) throws {
        var container = encoder.container(keyedBy: CodingKeys.self)
        try container.encodeIfPresent(name, forKey: .name)
        if clearDescription {
            try container.encodeNil(forKey: .description)
        } else {
            try container.encodeIfPresent(description, forKey: .description)
        }
        try container.encodeIfPresent(command, forKey: .command)
        try container.encodeIfPresent(args, forKey: .args)
        try container.encodeIfPresent(enabled, forKey: .enabled)
        try container.encodeIfPresent(isAgent, forKey: .isAgent)
    }
}

struct ActionsEnvelope: Decodable {
    var actions: [ATCAction]
}

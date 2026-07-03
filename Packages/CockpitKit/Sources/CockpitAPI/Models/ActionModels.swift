import Foundation

/// One entry from `GET /api/actions` (wrapped in `{"actions":[...]}`).
public struct CockpitAction: Codable, Sendable, Hashable, Identifiable {
    /// How a prompt is passed to the action; an empty/absent flag means
    /// positional. Presence of the spec at all means the action takes a prompt.
    public struct PromptSpec: Codable, Sendable, Hashable {
        public var flag: String?
    }

    /// Closed parameter definition: only "enum" and "bool" types exist.
    public struct ParamSpec: Codable, Sendable, Hashable {
        public var type: String
        public var values: [String]?
        public var `default`: JSONValue?
        public var flag: String?
        public var label: String?
        public var description: String?

        public var isEnum: Bool { type == "enum" }
        public var isBool: Bool { type == "bool" }
    }

    public var name: String
    /// custom | modified | builtin — drives client edit affordances.
    public var origin: String
    public var enabled: Bool
    public var label: String?
    public var description: String?
    public var prompt: PromptSpec?
    public var params: [String: ParamSpec]

    public var id: String { name }
    public var displayLabel: String { label ?? name }
    public var acceptsPrompt: Bool { prompt != nil }
}

struct ActionsEnvelope: Decodable {
    var actions: [CockpitAction]
}

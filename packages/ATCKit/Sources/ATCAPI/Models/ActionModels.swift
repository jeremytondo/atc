import Foundation

/// One entry from `GET /api/actions` (wrapped in `{"actions":[...]}`).
/// `GET /api/actions/{name}` and every mutation response return the same
/// shape plus `command`/`args`, which the list view omits.
public struct ATCAction: Codable, Sendable, Hashable, Identifiable {
    /// How a prompt is passed to the action; an empty/absent flag means
    /// positional. Presence of the spec at all means the action takes a prompt.
    public struct PromptSpec: Codable, Sendable, Hashable {
        public var flag: String?

        public init(flag: String? = nil) {
            self.flag = flag
        }
    }

    /// Closed parameter definition: only "enum" and "bool" types exist.
    public struct ParamSpec: Codable, Sendable, Hashable {
        public var type: String
        public var values: [String]?
        public var `default`: JSONValue?
        public var flag: String?
        public var label: String?
        public var description: String?

        public init(
            type: String,
            values: [String]? = nil,
            default: JSONValue? = nil,
            flag: String? = nil,
            label: String? = nil,
            description: String? = nil
        ) {
            self.type = type
            self.values = values
            self.default = `default`
            self.flag = flag
            self.label = label
            self.description = description
        }

        public var isEnum: Bool { type == "enum" }
        public var isBool: Bool { type == "bool" }
    }

    public var name: String
    /// "action" | "agent" — presentation metadata, immutable server-side.
    /// Optional so pre-type captures still decode; treat nil as "action".
    public var type: String?
    /// custom | modified | builtin — drives client edit affordances.
    public var origin: String
    public var enabled: Bool
    public var label: String?
    public var description: String?
    /// Executable and fixed argv — only in the detail response
    /// (`GET /api/actions/{name}`); the list omits them.
    public var command: String?
    public var args: [String]?
    public var prompt: PromptSpec?
    public var params: [String: ParamSpec]

    public init(
        name: String,
        type: String? = nil,
        origin: String,
        enabled: Bool,
        label: String? = nil,
        description: String? = nil,
        command: String? = nil,
        args: [String]? = nil,
        prompt: PromptSpec? = nil,
        params: [String: ParamSpec] = [:]
    ) {
        self.name = name
        self.type = type
        self.origin = origin
        self.enabled = enabled
        self.label = label
        self.description = description
        self.command = command
        self.args = args
        self.prompt = prompt
        self.params = params
    }

    public var id: String { name }
    public var displayLabel: String { label ?? name }
    public var acceptsPrompt: Bool { prompt != nil }
    public var isBuiltin: Bool { origin == "builtin" }
    public var isModified: Bool { origin == "modified" }
    public var isCustom: Bool { origin == "custom" }
    public var isAgent: Bool { type == "agent" }
}

/// Body for `POST /api/actions` and `PUT /api/actions/{name}`. PUT is a
/// full replace on the server, so callers must round-trip every field of
/// the existing action, not just the ones they changed.
public struct ActionWriteRequest: Codable, Sendable, Hashable {
    /// Optional on create — the server derives it from `label` via slugify.
    /// On PUT it must match the route name if present.
    public var name: String?
    /// "action" | "agent". Defaults to "action" on create when omitted;
    /// immutable on update (an omitted type inherits the current one).
    public var type: String?
    public var label: String?
    public var description: String?
    public var command: String
    public var args: [String]?
    /// Omit entirely (nil) for a no-prompt action; `PromptSpec()` means
    /// positional prompt.
    public var prompt: ATCAction.PromptSpec?
    public var params: [String: ATCAction.ParamSpec]?
    public var enabled: Bool?

    public init(
        name: String? = nil,
        type: String? = nil,
        label: String? = nil,
        description: String? = nil,
        command: String,
        args: [String]? = nil,
        prompt: ATCAction.PromptSpec? = nil,
        params: [String: ATCAction.ParamSpec]? = nil,
        enabled: Bool? = nil
    ) {
        self.name = name
        self.type = type
        self.label = label
        self.description = description
        self.command = command
        self.args = args
        self.prompt = prompt
        self.params = params
        self.enabled = enabled
    }
}

/// Client-side mirror of the server's action-name rules, so forms can
/// validate and preview the derived name before a request is made.
public enum ActionName {
    /// Names must match `^[A-Za-z0-9_-]+$` (server: actionNameRE).
    public static func isValid(_ name: String) -> Bool {
        !name.isEmpty && name.unicodeScalars.allSatisfy { scalar in
            ("A"..."Z").contains(scalar) || ("a"..."z").contains(scalar)
                || ("0"..."9").contains(scalar) || scalar == "_" || scalar == "-"
        }
    }

    /// Mirrors the server's `Slugify`: lowercase, runs of anything outside
    /// [a-z0-9] collapse to a single "-", leading/trailing dashes trimmed.
    public static func slugify(_ raw: String) -> String {
        var result = ""
        var pendingDash = false
        for character in raw.lowercased() {
            if character.isASCII && (character.isLetter || character.isNumber) {
                if pendingDash && !result.isEmpty {
                    result.append("-")
                }
                pendingDash = false
                result.append(character)
            } else {
                pendingDash = true
            }
        }
        return result
    }
}

struct ActionsEnvelope: Decodable {
    var actions: [ATCAction]
}

import Foundation

/// One entry from `GET /api/environments` (wrapped in `{"environments":[...]}`).
public struct AtelierCodeEnvironment: Codable, Sendable, Hashable, Identifiable {
    public var name: String
    public var kind: String
    public var label: String?
    public var description: String?
    public var isDefault: Bool

    public var id: String { name }
    public var displayLabel: String { label ?? name }

    enum CodingKeys: String, CodingKey {
        case name, kind, label, description
        case isDefault = "default"
    }

    public init(from decoder: any Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        name = try container.decode(String.self, forKey: .name)
        kind = try container.decode(String.self, forKey: .kind)
        label = try container.decodeIfPresent(String.self, forKey: .label)
        description = try container.decodeIfPresent(String.self, forKey: .description)
        isDefault = try container.decodeIfPresent(Bool.self, forKey: .isDefault) ?? false
    }
}

struct EnvironmentsEnvelope: Decodable {
    var environments: [AtelierCodeEnvironment]
}

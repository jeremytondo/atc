import Foundation

struct StartupEntry: Identifiable, Codable, Equatable {
    enum Target: Codable, Equatable {
        case action(id: String)
        case shell
    }

    let id: UUID
    let target: Target
    var customName: String?

    init(id: UUID = UUID(), target: Target, customName: String? = nil) {
        self.id = id
        self.target = target
        self.customName = Self.normalized(customName)
    }

    mutating func setCustomName(_ name: String) {
        customName = Self.normalized(name)
    }

    /// Interior/trailing spaces are preserved so live text-field bindings can
    /// type through; only an effectively blank name collapses to nil.
    private static func normalized(_ name: String?) -> String? {
        guard let name,
              !name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        else { return nil }
        return name
    }
}

/// Value-semantic startup configuration whose mutations preserve the single
/// Default Session invariant. Decoding also repairs stale or malformed
/// default references instead of allowing invalid persisted state into the app.
struct StartupConfiguration: Codable, Equatable {
    private(set) var entries: [StartupEntry]
    private(set) var defaultEntryID: UUID?

    static let empty = StartupConfiguration()

    init(entries: [StartupEntry] = [], defaultEntryID: UUID? = nil) {
        self.entries = entries
        self.defaultEntryID = Self.validDefault(in: entries, requested: defaultEntryID)
    }

    @discardableResult
    mutating func add(
        target: StartupEntry.Target,
        customName: String? = nil,
        id: UUID = UUID()
    ) -> UUID {
        let entry = StartupEntry(id: id, target: target, customName: customName)
        entries.append(entry)
        if entries.count == 1 {
            defaultEntryID = entry.id
        }
        return entry.id
    }

    mutating func remove(id: UUID) {
        guard let index = entries.firstIndex(where: { $0.id == id }) else { return }
        let removedDefault = defaultEntryID == id
        entries.remove(at: index)
        if entries.isEmpty {
            defaultEntryID = nil
        } else if removedDefault {
            defaultEntryID = entries.first?.id
        }
    }

    mutating func transferDefault(to id: UUID) {
        guard entries.contains(where: { $0.id == id }) else { return }
        defaultEntryID = id
    }

    mutating func setCustomName(_ name: String, for id: UUID) {
        guard let index = entries.firstIndex(where: { $0.id == id }) else { return }
        entries[index].setCustomName(name)
    }

    mutating func removeAll() {
        entries = []
        defaultEntryID = nil
    }

    private enum CodingKeys: String, CodingKey {
        case entries
        case defaultEntryID
    }

    init(from decoder: any Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        let entries = try container.decode([StartupEntry].self, forKey: .entries)
        let requestedDefault = try container.decodeIfPresent(UUID.self, forKey: .defaultEntryID)
        self.init(entries: entries, defaultEntryID: requestedDefault)
    }

    private static func validDefault(in entries: [StartupEntry], requested: UUID?) -> UUID? {
        guard !entries.isEmpty else { return nil }
        if let requested, entries.contains(where: { $0.id == requested }) {
            return requested
        }
        return entries[0].id
    }
}

enum ProjectStartupMode: String, Codable, CaseIterable, Equatable {
    case useConnectionDefaults
    case custom
}

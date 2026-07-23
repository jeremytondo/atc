import Foundation
import ATCAPI

enum StartupEntryAvailability: Equatable {
    case valid
    case disabled
    case missing
    case unableToValidate
}

struct ValidatedStartupEntry: Identifiable, Equatable {
    let entryID: UUID
    let availability: StartupEntryAvailability
    let cachedActionName: String?

    var id: UUID { entryID }
}

struct StartupConfigurationValidation: Equatable {
    let entries: [ValidatedStartupEntry]
    let canEdit: Bool

    func entry(id: UUID) -> ValidatedStartupEntry? {
        entries.first { $0.entryID == id }
    }
}

enum StartupEntryValidator {
    /// Validates strictly against the current cache snapshot. Callers decide
    /// reachability from the owning runtime; this function never causes I/O.
    static func validate(
        configuration: StartupConfiguration,
        actions: [ATCAction],
        hasLoadedOnce: Bool,
        isReachable: Bool
    ) -> StartupConfigurationValidation {
        let canEdit = hasLoadedOnce && isReachable
        let actionsByID = Dictionary(uniqueKeysWithValues: actions.map { ($0.id, $0) })
        let entries = configuration.entries.map { entry in
            switch entry.target {
            case .shell:
                return ValidatedStartupEntry(
                    entryID: entry.id,
                    availability: canEdit ? .valid : .unableToValidate,
                    cachedActionName: nil
                )
            case .action(let id):
                let action = actionsByID[id]
                let availability: StartupEntryAvailability
                if !canEdit {
                    availability = .unableToValidate
                } else if let action {
                    availability = action.enabled ? .valid : .disabled
                } else {
                    availability = .missing
                }
                return ValidatedStartupEntry(
                    entryID: entry.id,
                    availability: availability,
                    cachedActionName: action?.name
                )
            }
        }
        return StartupConfigurationValidation(entries: entries, canEdit: canEdit)
    }
}

import Foundation
import Observation
import ATCAPI

/// Editable fields for one complete action definition.
@MainActor
@Observable
final class ActionDraft {
    var name = ""
    var descriptionText = ""
    var command = ""
    /// One literal argument per line. Spaces within an argument are kept.
    var argsText = ""
    var isAgent = false
    var enabled = true

    init() {}

    init(action: ATCAction) {
        name = action.name
        descriptionText = action.description ?? ""
        command = action.command
        argsText = action.args.joined(separator: "\n")
        isAgent = action.isAgent
        enabled = action.enabled
    }

    var args: [String] {
        argsText.components(separatedBy: .newlines)
            .filter { !$0.isEmpty }
    }

    func validationMessage() -> String? {
        if normalizedName.isEmpty {
            return "Enter a name for this action."
        }
        if normalizedCommand.isEmpty {
            return "Enter the command this action launches."
        }
        return nil
    }

    func createRequest() -> ActionCreate {
        ActionCreate(
            name: normalizedName,
            description: Self.trimmedOrNil(descriptionText),
            command: normalizedCommand,
            args: args,
            enabled: enabled,
            isAgent: isAgent
        )
    }

    /// The editor sends every mutable field. An empty description explicitly
    /// clears a previously stored value.
    func patch() -> ActionPatch {
        let description = Self.trimmedOrNil(descriptionText)
        return ActionPatch(
            name: normalizedName,
            description: description,
            clearDescription: description == nil,
            command: normalizedCommand,
            args: args,
            enabled: enabled,
            isAgent: isAgent
        )
    }

    private var normalizedName: String {
        name.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    private var normalizedCommand: String {
        command.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    private static func trimmedOrNil(_ text: String) -> String? {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        return trimmed.isEmpty ? nil : trimmed
    }
}

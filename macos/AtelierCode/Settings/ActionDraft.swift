import Foundation
import Observation
import AtelierCodeAPI

/// Editable draft of one action definition, convertible to the write
/// request. PUT is a full replace on the server, so the draft carries every
/// field — including params — and always sends them all back.
@MainActor
@Observable
final class ActionDraft {
    /// Only editable for new actions; the server has no rename.
    var name = ""
    var label = ""
    var descriptionText = ""
    var command = ""
    /// One argument per line — args may contain spaces, so newline is the
    /// only safe separator.
    var argsText = ""
    var acceptsPrompt = false
    var promptFlag = ""
    var enabled = true
    var params: [ParamDraft] = []

    init() {}

    init(action: AtelierCodeAction) {
        name = action.name
        label = action.label ?? ""
        descriptionText = action.description ?? ""
        command = action.command ?? ""
        argsText = (action.args ?? []).joined(separator: "\n")
        acceptsPrompt = action.prompt != nil
        promptFlag = action.prompt?.flag ?? ""
        enabled = action.enabled
        params = action.params
            .sorted { $0.key < $1.key }
            .map { ParamDraft(name: $0.key, spec: $0.value) }
    }

    /// The name a create would use: explicit, else derived from the label
    /// the way the server does.
    var derivedName: String {
        let trimmed = name.trimmingCharacters(in: .whitespaces)
        return trimmed.isEmpty ? ActionName.slugify(label) : trimmed
    }

    var args: [String] {
        argsText.split(separator: "\n")
            .map { $0.trimmingCharacters(in: .whitespaces) }
            .filter { !$0.isEmpty }
    }

    /// First problem that would make the server reject the write, or nil.
    /// `isNew` adds the name rules that only apply on create.
    func validationMessage(isNew: Bool) -> String? {
        if command.trimmingCharacters(in: .whitespaces).isEmpty {
            return "Enter the command this action launches."
        }
        if isNew {
            if derivedName.isEmpty {
                return "Enter a name, or a label to derive one from."
            }
            if !ActionName.isValid(derivedName) {
                return "Action names can only use letters, numbers, dashes, and underscores."
            }
        }
        var seen = Set<String>()
        for param in params {
            let paramName = param.trimmedName
            if paramName.isEmpty {
                return "Every parameter needs a name."
            }
            if !seen.insert(paramName).inserted {
                return "Parameter names must be unique — “\(paramName)” repeats."
            }
            if param.isEnum && param.values.isEmpty {
                return "Parameter “\(paramName)” needs at least one value."
            }
            if param.isEnum, !param.defaultValue.isEmpty, !param.values.contains(param.defaultValue) {
                return "Parameter “\(paramName)”’s default must be one of its values."
            }
            if !param.isEnum, param.boolDefault, param.flag.trimmingCharacters(in: .whitespaces).isEmpty {
                return "Parameter “\(paramName)” needs a flag because it defaults to on."
            }
        }
        return nil
    }

    /// Full-replace request body. Pass the route name on updates (the server
    /// requires body name == route name); pass nil on create, where an empty
    /// name field is omitted so the server derives one from the label.
    func writeRequest(routeName: String?) -> ActionWriteRequest {
        let trimmedName = name.trimmingCharacters(in: .whitespaces)
        var specs: [String: AtelierCodeAction.ParamSpec] = [:]
        for param in params {
            specs[param.trimmedName] = param.spec
        }
        let trimmedFlag = promptFlag.trimmingCharacters(in: .whitespaces)
        return ActionWriteRequest(
            name: routeName ?? (trimmedName.isEmpty ? nil : trimmedName),
            label: Self.trimmedOrNil(label),
            description: Self.trimmedOrNil(descriptionText),
            command: command.trimmingCharacters(in: .whitespaces),
            args: args,
            prompt: acceptsPrompt
                ? .init(flag: trimmedFlag.isEmpty ? nil : trimmedFlag)
                : nil,
            params: specs,
            enabled: enabled
        )
    }

    static func trimmedOrNil(_ text: String) -> String? {
        let trimmed = text.trimmingCharacters(in: .whitespaces)
        return trimmed.isEmpty ? nil : trimmed
    }
}

/// Draft of one entry in an action's `params` map. Only the server's two
/// closed types exist: "enum" (a choice list) and "bool" (an on/off switch).
@MainActor
@Observable
final class ParamDraft: Identifiable {
    let id = UUID()
    var name = ""
    var isEnum = true
    /// Comma-separated choice values (enum only).
    var valuesText = ""
    /// Enum default; empty means none.
    var defaultValue = ""
    var boolDefault = false
    var flag = ""
    var label = ""
    var descriptionText = ""

    init() {}

    init(name: String, spec: AtelierCodeAction.ParamSpec) {
        self.name = name
        isEnum = spec.isEnum
        valuesText = (spec.values ?? []).joined(separator: ", ")
        switch (spec.isEnum, spec.default) {
        case (true, .string(let value)):
            defaultValue = value
        case (false, .bool(let value)):
            boolDefault = value
        case (false, .string(let value)):
            // The server accepts string bools ("true"/"false") too.
            boolDefault = value == "true"
        default:
            break
        }
        flag = spec.flag ?? ""
        label = spec.label ?? ""
        descriptionText = spec.description ?? ""
    }

    var trimmedName: String {
        name.trimmingCharacters(in: .whitespaces)
    }

    var values: [String] {
        valuesText.split(separator: ",")
            .map { $0.trimmingCharacters(in: .whitespaces) }
            .filter { !$0.isEmpty }
    }

    /// `values` with duplicates dropped — stable IDs for the default picker.
    var uniqueValues: [String] {
        var seen = Set<String>()
        return values.filter { seen.insert($0).inserted }
    }

    var spec: AtelierCodeAction.ParamSpec {
        if isEnum {
            return .init(
                type: "enum",
                values: values,
                default: defaultValue.isEmpty ? nil : .string(defaultValue),
                flag: ActionDraft.trimmedOrNil(flag),
                label: ActionDraft.trimmedOrNil(label),
                description: ActionDraft.trimmedOrNil(descriptionText)
            )
        }
        return .init(
            type: "bool",
            // A false default and no default are equivalent; omit.
            default: boolDefault ? .bool(true) : nil,
            flag: ActionDraft.trimmedOrNil(flag),
            label: ActionDraft.trimmedOrNil(label),
            description: ActionDraft.trimmedOrNil(descriptionText)
        )
    }
}

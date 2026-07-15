import Foundation

struct KeyStroke: Hashable, Sendable, CustomStringConvertible {
    struct Modifiers: OptionSet, Hashable, Sendable {
        let rawValue: UInt8

        static let command = Modifiers(rawValue: 1 << 0)
        static let control = Modifiers(rawValue: 1 << 1)
        static let option = Modifiers(rawValue: 1 << 2)
        static let shift = Modifiers(rawValue: 1 << 3)

        static let primary: Modifiers = [.command, .control, .option]
    }

    let key: String
    let modifiers: Modifiers

    static let escape = KeyStroke(key: "escape", modifiers: [])

    var description: String {
        var parts: [String] = []
        if modifiers.contains(.command) { parts.append("cmd") }
        if modifiers.contains(.control) { parts.append("ctrl") }
        if modifiers.contains(.option) { parts.append("opt") }
        if modifiers.contains(.shift) { parts.append("shift") }
        parts.append(key)
        return parts.joined(separator: "+")
    }

    var displayDescription: String {
        var value = ""
        if modifiers.contains(.control) { value += "⌃" }
        if modifiers.contains(.option) { value += "⌥" }
        if modifiers.contains(.shift) { value += "⇧" }
        if modifiers.contains(.command) { value += "⌘" }
        value += key == "escape" ? "Esc" : key.uppercased()
        return value
    }

    var hasPrimaryModifier: Bool {
        !modifiers.intersection(.primary).isEmpty
    }

    static func parse(_ text: String) -> Result<KeyStroke, TriggerError> {
        parse(text, requiresPrimaryModifier: true)
    }

    static func parseContinuation(_ text: String) -> Result<KeyStroke, TriggerError> {
        parse(text, requiresPrimaryModifier: false)
    }

    private static func parse(
        _ text: String,
        requiresPrimaryModifier: Bool
    ) -> Result<KeyStroke, TriggerError> {
        let tokens = text.split(separator: "+", omittingEmptySubsequences: false)
            .map { $0.trimmingCharacters(in: .whitespacesAndNewlines).lowercased() }
        guard let keyToken = tokens.last, !keyToken.isEmpty else {
            return .failure(.init(message: "Trigger has an empty key"))
        }

        var modifiers: Modifiers = []
        for token in tokens.dropLast() {
            guard !token.isEmpty else {
                return .failure(.init(message: "Trigger contains an empty modifier token"))
            }
            let modifier: Modifiers
            switch token {
            case "cmd", "command", "super": modifier = .command
            case "ctrl", "control": modifier = .control
            case "opt", "option", "alt": modifier = .option
            case "shift": modifier = .shift
            default:
                return .failure(.init(message: "Unknown modifier token '\(token)'"))
            }
            guard !modifiers.contains(modifier) else {
                return .failure(.init(message: "Duplicate modifier token '\(token)'"))
            }
            modifiers.insert(modifier)
        }

        guard keyToken != "escape" else {
            return .failure(.init(
                message: "Named key 'escape' is deferred beyond the MVP"
            ))
        }
        guard keyToken.count == 1,
              let scalar = keyToken.unicodeScalars.first,
              isPrintable(scalar)
        else {
            let kind = keyToken.hasPrefix("f") && Int(keyToken.dropFirst()) != nil
                ? "Function key '\(keyToken)'"
                : "Key '\(keyToken)'"
            return .failure(.init(message: "\(kind) is deferred beyond the MVP"))
        }
        let stroke = KeyStroke(key: keyToken, modifiers: modifiers)
        guard !requiresPrimaryModifier || stroke.hasPrimaryModifier else {
            let detail = modifiers == [.shift] ? "shift-only" : "unmodified"
            return .failure(.init(
                message: "A \(detail) direct trigger is deferred beyond the MVP"
            ))
        }
        return .success(stroke)
    }

    private static func isPrintable(_ scalar: UnicodeScalar) -> Bool {
        !CharacterSet.controlCharacters.contains(scalar)
            && !CharacterSet.illegalCharacters.contains(scalar)
            && !(0xF700...0xF8FF).contains(scalar.value)
    }
}

typealias KeySequence = [KeyStroke]

struct TriggerError: Error, Equatable, CustomStringConvertible {
    let message: String
    var description: String { message }
}

enum ParsedKeySequence: Equatable {
    case direct(KeyStroke)
    case leader(continuation: KeyStroke)

    static func parse(_ text: String) -> Result<ParsedKeySequence, TriggerError> {
        let steps = text.split(separator: ">", omittingEmptySubsequences: false)
            .map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }
        switch steps.count {
        case 1:
            guard steps[0].lowercased() != "leader" else {
                return .failure(.init(
                    message: "'leader' is reserved for the first step of leader>X"
                ))
            }
            return KeyStroke.parse(steps[0]).map(ParsedKeySequence.direct)
        case 2:
            guard steps[0].lowercased() == "leader" else {
                return .failure(.init(
                    message: "Only leader>X sequences are supported in the MVP"
                ))
            }
            guard steps[1].lowercased() != "leader" else {
                return .failure(.init(
                    message: "'leader' is only valid as the first sequence step"
                ))
            }
            return KeyStroke.parseContinuation(steps[1]).map {
                .leader(continuation: $0)
            }
        default:
            return .failure(.init(
                message: "Only two-step leader>X sequences are supported in the MVP"
            ))
        }
    }
}

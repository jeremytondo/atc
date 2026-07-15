import Foundation

struct ConfigDiagnostic: Error, Equatable, CustomStringConvertible, Sendable {
    enum Severity: Equatable, Sendable {
        case error
        case warning
    }

    let severity: Severity
    let line: Int?
    let message: String

    var description: String {
        let location = line.map { "line \($0): " } ?? ""
        return "\(severity == .error ? "error" : "warning"): \(location)\(message)"
    }
}

struct ConfigDiagnostics: Error, Equatable, RandomAccessCollection, Sendable {
    private(set) var diagnostics: [ConfigDiagnostic]

    init(_ diagnostics: [ConfigDiagnostic]) {
        self.diagnostics = diagnostics
    }

    var startIndex: Int { diagnostics.startIndex }
    var endIndex: Int { diagnostics.endIndex }
    subscript(position: Int) -> ConfigDiagnostic { diagnostics[position] }
}

struct ParsedConfig: Sendable {
    enum Value: Equatable, Sendable {
        case string(String)
        case integer(Int)
        case boolean(Bool)
    }

    struct Entry: Equatable, Sendable {
        let key: String
        let value: Value
        let line: Int
    }

    let tables: [String: [Entry]]
    let diagnostics: [ConfigDiagnostic]

    static let empty = ParsedConfig(tables: [:], diagnostics: [])
}

enum KeyboardConfigParser {
    private static let recognizedTables = ["keyboard", "keybindings"]
    private static let keyboardKeys = [
        "leader", "leader_timeout_ms", "clear_default_keybindings",
    ]

    static func parse(data: Data) -> ParsedConfig {
        guard let text = String(data: data, encoding: .utf8) else {
            return ParsedConfig(
                tables: [:],
                diagnostics: [.init(
                    severity: .error,
                    line: nil,
                    message: "config.toml is not valid UTF-8"
                )]
            )
        }
        return parse(text)
    }

    static func parse(_ text: String) -> ParsedConfig {
        var tables: [String: [ParsedConfig.Entry]] = [:]
        var diagnostics: [ConfigDiagnostic] = []
        var currentTable: String?
        var seen: [String: Set<String>] = [:]

        for (offset, rawLine) in text.split(
            separator: "\n",
            omittingEmptySubsequences: false
        ).enumerated() {
            let lineNumber = offset + 1
            let uncommented = removingComment(from: String(rawLine))
            let line = uncommented.trimmingCharacters(in: .whitespacesAndNewlines)
            guard !line.isEmpty else { continue }

            if line.hasPrefix("[") {
                guard line.hasSuffix("]"),
                      !line.hasPrefix("[["),
                      !line.hasSuffix("]]"),
                      line.filter({ $0 == "[" }).count == 1,
                      line.filter({ $0 == "]" }).count == 1
                else {
                    diagnostics.append(error(lineNumber, "Unsupported table construct '\(line)'"))
                    currentTable = nil
                    continue
                }
                let name = String(line.dropFirst().dropLast())
                    .trimmingCharacters(in: .whitespaces)
                guard isBareKey(name) else {
                    diagnostics.append(error(
                        lineNumber,
                        "Table name '\(name)' must be a bare name"
                    ))
                    currentTable = nil
                    continue
                }
                currentTable = name
                continue
            }

            if let currentTable, !recognizedTables.contains(currentTable) {
                // Unknown top-level tables and their contents belong to
                // other subsystems sharing config.toml.
                continue
            }

            guard let equals = firstUnquotedEquals(in: line) else {
                diagnostics.append(error(lineNumber, "Expected key = value"))
                continue
            }
            let keyText = String(line[..<equals]).trimmingCharacters(in: .whitespaces)
            let valueText = String(line[line.index(after: equals)...])
                .trimmingCharacters(in: .whitespaces)
            guard let table = currentTable else {
                diagnostics.append(error(
                    lineNumber,
                    "Top-level values are unsupported; place the key in a table"
                ))
                continue
            }

            let keyResult = parseKey(keyText, line: lineNumber)
            let valueResult = parseValue(valueText, line: lineNumber)
            guard case .success(let key) = keyResult,
                  case .success(let value) = valueResult
            else {
                if case .failure(let diagnostic) = keyResult { diagnostics.append(diagnostic) }
                if case .failure(let diagnostic) = valueResult { diagnostics.append(diagnostic) }
                continue
            }

            if seen[table, default: []].contains(key) {
                diagnostics.append(.init(
                    severity: .warning,
                    line: lineNumber,
                    message: "Duplicate key '\(key)' in [\(table)]; the later entry wins"
                ))
            }
            seen[table, default: []].insert(key)
            tables[table, default: []].append(.init(
                key: key,
                value: value,
                line: lineNumber
            ))
            if table == "keyboard", !keyboardKeys.contains(key) {
                diagnostics.append(.init(
                    severity: .warning,
                    line: lineNumber,
                    message: "Unknown [keyboard] key '\(key)' was ignored"
                ))
            }
        }

        return ParsedConfig(tables: tables, diagnostics: diagnostics)
    }

    private static func parseKey(
        _ text: String,
        line: Int
    ) -> Result<String, ConfigDiagnostic> {
        guard !text.isEmpty else {
            return .failure(error(line, "Key cannot be empty"))
        }
        if text.hasPrefix("\"") {
            return parseBasicString(text, line: line)
        }
        if text.hasPrefix("'") {
            return .failure(error(
                line,
                "Literal strings are unsupported; use a basic quoted string"
            ))
        }
        if text.contains(".") {
            return .failure(error(
                line,
                "Dotted keys are unsupported: '\(text)'"
            ))
        }
        guard isBareKey(text) else {
            return .failure(error(
                line,
                "Invalid bare key '\(text)'"
            ))
        }
        return .success(text)
    }

    private static func parseValue(
        _ text: String,
        line: Int
    ) -> Result<ParsedConfig.Value, ConfigDiagnostic> {
        guard !text.isEmpty else {
            return .failure(error(line, "Value cannot be empty"))
        }
        if text.hasPrefix("\"\"\"") || text.hasPrefix("'''") {
            return .failure(error(line, "Multiline strings are unsupported"))
        }
        if text.hasPrefix("\"") {
            return parseBasicString(text, line: line)
                .map(ParsedConfig.Value.string)
        }
        if text.hasPrefix("'") {
            return .failure(error(
                line,
                "Literal strings are unsupported; use a basic quoted string"
            ))
        }
        if text.hasPrefix("[") {
            return .failure(error(line, "Arrays are unsupported"))
        }
        if text.hasPrefix("{") {
            return .failure(error(line, "Inline tables are unsupported"))
        }
        if text == "true" { return .success(.boolean(true)) }
        if text == "false" { return .success(.boolean(false)) }
        if text.range(of: #"^[+-]?[0-9]+$"#, options: .regularExpression) != nil,
           let value = Int(text) {
            return .success(.integer(value))
        }
        if text.range(of: #"^[+-]?[0-9]+\.[0-9]+$"#, options: .regularExpression) != nil {
            return .failure(error(line, "Floats are unsupported"))
        }
        if text.range(of: #"^[0-9]{4}-[0-9]{2}-[0-9]{2}"#, options: .regularExpression) != nil {
            return .failure(error(line, "Dates are unsupported"))
        }
        return .failure(error(
            line,
            "Unsupported value construct '\(text)'"
        ))
    }

    private static func parseBasicString(
        _ text: String,
        line: Int
    ) -> Result<String, ConfigDiagnostic> {
        guard text.count >= 2, text.first == "\"", text.last == "\"" else {
            return .failure(error(line, "Unterminated basic quoted string"))
        }
        let body = text.dropFirst().dropLast()
        var result = ""
        var index = body.startIndex
        while index < body.endIndex {
            let character = body[index]
            guard character == "\\" else {
                if character == "\"" {
                    return .failure(error(line, "Unescaped quote in basic string"))
                }
                result.append(character)
                index = body.index(after: index)
                continue
            }
            index = body.index(after: index)
            guard index < body.endIndex else {
                return .failure(error(line, "Incomplete escape in basic string"))
            }
            let escape = body[index]
            switch escape {
            case "\"": result.append("\"")
            case "\\": result.append("\\")
            case "n": result.append("\n")
            case "t": result.append("\t")
            case "u":
                let start = body.index(after: index)
                guard let end = body.index(start, offsetBy: 4, limitedBy: body.endIndex),
                      body.distance(from: start, to: end) == 4,
                      let scalarValue = UInt32(body[start..<end], radix: 16),
                      let scalar = UnicodeScalar(scalarValue)
                else {
                    return .failure(error(line, "Invalid \\uXXXX escape"))
                }
                result.unicodeScalars.append(scalar)
                index = body.index(before: end)
            default:
                return .failure(error(line, "Unsupported escape '\\\(escape)'"))
            }
            index = body.index(after: index)
        }
        return .success(result)
    }

    private static func removingComment(from line: String) -> String {
        var quoted = false
        var escaped = false
        for index in line.indices {
            let character = line[index]
            if escaped {
                escaped = false
                continue
            }
            if quoted, character == "\\" {
                escaped = true
            } else if character == "\"" {
                quoted.toggle()
            } else if character == "#", !quoted {
                return String(line[..<index])
            }
        }
        return line
    }

    private static func firstUnquotedEquals(in line: String) -> String.Index? {
        var quoted = false
        var escaped = false
        for index in line.indices {
            let character = line[index]
            if escaped {
                escaped = false
            } else if quoted, character == "\\" {
                escaped = true
            } else if character == "\"" {
                quoted.toggle()
            } else if character == "=", !quoted {
                return index
            }
        }
        return nil
    }

    private static func isBareKey(_ text: String) -> Bool {
        !text.isEmpty && text.utf8.allSatisfy {
            (48...57).contains($0) || (65...90).contains($0)
                || (97...122).contains($0) || $0 == 95 || $0 == 45
        }
    }

    private static func error(_ line: Int, _ message: String) -> ConfigDiagnostic {
        ConfigDiagnostic(severity: .error, line: line, message: message)
    }
}

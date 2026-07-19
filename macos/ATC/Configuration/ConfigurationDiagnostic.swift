import Foundation

struct ConfigDiagnostic: Error, Equatable, CustomStringConvertible, Sendable {
    enum Severity: Equatable, Sendable {
        case error
        case warning
    }

    let severity: Severity
    let message: String

    // TOML syntax errors carry their line inside the parser's message;
    // semantic messages name the table/key instead.
    var description: String {
        "\(severity == .error ? "error" : "warning"): \(message)"
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

func configurationKeyPath(table: String, key: String) -> String {
    if key.allSatisfy({ $0.isLetter || $0.isNumber || $0 == "_" || $0 == "-" }) {
        return "[\(table)].\(key)"
    }
    let escaped = key
        .replacingOccurrences(of: "\\", with: "\\\\")
        .replacingOccurrences(of: "\"", with: "\\\"")
    return "[\(table)].\"\(escaped)\""
}

import Foundation
import TOMLDecoder

struct ParsedConfig: Sendable {
    enum Value: Equatable, Sendable {
        case string(String)
        case integer(Int)
        case boolean(Bool)
    }

    struct Entry: Equatable, Sendable {
        let key: String
        let value: Value
    }

    let tables: [String: [Entry]]
    let diagnostics: [ConfigDiagnostic]

    static let empty = ParsedConfig(tables: [:], diagnostics: [])
}

enum ConfigurationLoader {
    private static let recognizedTables = Set(["keyboard", "keybindings"])
    private static let keyboardKeys = Set([
        "leader", "leader_timeout_ms", "clear_default_keybindings",
    ])

    static func parse(data: Data) -> ParsedConfig {
        guard let text = String(data: data, encoding: .utf8) else {
            return ParsedConfig(
                tables: [:],
                diagnostics: [diagnostic("macos.toml is not valid UTF-8")]
            )
        }
        return parse(text)
    }

    static func parse(_ text: String) -> ParsedConfig {
        let root: TOMLTable
        do {
            root = try TOMLTable(source: text)
        } catch {
            return ParsedConfig(
                tables: [:],
                diagnostics: [diagnostic(error.description)]
            )
        }

        var diagnostics: [ConfigDiagnostic] = []
        var tables: [String: [ParsedConfig.Entry]] = [:]

        for key in root.keys.sorted() {
            guard recognizedTables.contains(key) else {
                do {
                    _ = try root.table(forKey: key)
                    diagnostics.append(diagnostic(
                        "[\(key)] is not a recognized table (expected [keyboard] or [keybindings])"
                    ))
                } catch {
                    diagnostics.append(diagnostic(
                        "Top-level key '\(key)' is unsupported; expected [keyboard] or [keybindings]"
                    ))
                }
                continue
            }

            let table: TOMLTable
            do {
                table = try root.table(forKey: key)
            } catch {
                diagnostics.append(diagnostic("[\(key)] must be a table"))
                continue
            }

            switch key {
            case "keyboard":
                tables[key] = decodeKeyboard(table, diagnostics: &diagnostics)
            case "keybindings":
                tables[key] = decodeKeybindings(table, diagnostics: &diagnostics)
            default:
                preconditionFailure("Recognized configuration table is not decoded")
            }
        }

        return ParsedConfig(tables: tables, diagnostics: diagnostics)
    }

    private static func decodeKeyboard(
        _ table: TOMLTable,
        diagnostics: inout [ConfigDiagnostic]
    ) -> [ParsedConfig.Entry] {
        var entries: [ParsedConfig.Entry] = []
        for key in table.keys.sorted() {
            guard keyboardKeys.contains(key) else {
                diagnostics.append(diagnostic(
                    "\(configurationKeyPath(table: "keyboard", key: key)) is not recognized"
                ))
                continue
            }

            switch key {
            case "leader":
                do {
                    entries.append(.init(
                        key: key,
                        value: .string(try table.string(forKey: key))
                    ))
                } catch {
                    diagnostics.append(diagnostic("[keyboard].leader must be a string"))
                }
            case "leader_timeout_ms":
                do {
                    let value = try table.integer(forKey: key)
                    entries.append(.init(key: key, value: .integer(Int(value))))
                } catch {
                    diagnostics.append(diagnostic(
                        "[keyboard].leader_timeout_ms must be an integer"
                    ))
                }
            case "clear_default_keybindings":
                do {
                    entries.append(.init(
                        key: key,
                        value: .boolean(try table.bool(forKey: key))
                    ))
                } catch {
                    diagnostics.append(diagnostic(
                        "[keyboard].clear_default_keybindings must be a boolean"
                    ))
                }
            default:
                preconditionFailure("Recognized keyboard key is not decoded")
            }
        }
        return entries
    }

    private static func decodeKeybindings(
        _ table: TOMLTable,
        diagnostics: inout [ConfigDiagnostic]
    ) -> [ParsedConfig.Entry] {
        // TOMLTable does not guarantee source order. Sorting makes binding
        // resolution and menu-shortcut selection deterministic.
        table.keys.sorted().compactMap { key in
            do {
                return ParsedConfig.Entry(
                    key: key,
                    value: .string(try table.string(forKey: key))
                )
            } catch {
                diagnostics.append(diagnostic(
                    "\(configurationKeyPath(table: "keybindings", key: key)) must be a string command id or \"unbind\""
                ))
                return nil
            }
        }
    }

    private static func diagnostic(_ message: String) -> ConfigDiagnostic {
        ConfigDiagnostic(severity: .error, message: message)
    }
}

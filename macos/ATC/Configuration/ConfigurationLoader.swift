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
    let terminal: TerminalPreferences
    let diagnostics: [ConfigDiagnostic]

    static let empty = ParsedConfig(tables: [:], terminal: .init(), diagnostics: [])
}

enum ConfigurationLoader {
    private static let recognizedTables = Set(["keyboard", "keybindings", "terminal"])
    private static let keyboardKeys = Set([
        "leader", "clear_default_keybindings",
    ])
    private static let terminalKeys = Set([
        "theme", "font_family", "font_size", "padding_x", "padding_y",
    ])

    static func parse(data: Data) -> ParsedConfig {
        guard let text = String(data: data, encoding: .utf8) else {
            return ParsedConfig(
                tables: [:],
                terminal: .init(),
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
                terminal: .init(),
                diagnostics: [diagnostic(error.description)]
            )
        }

        var diagnostics: [ConfigDiagnostic] = []
        var tables: [String: [ParsedConfig.Entry]] = [:]
        var terminal = TerminalPreferences()

        for key in root.keys.sorted() {
            guard recognizedTables.contains(key) else {
                do {
                    _ = try root.table(forKey: key)
                    diagnostics.append(diagnostic(
                        "[\(key)] is not a recognized table (expected [keyboard], [keybindings], or [terminal])"
                    ))
                } catch {
                    diagnostics.append(diagnostic(
                        "Top-level key '\(key)' is unsupported; expected [keyboard], [keybindings], or [terminal]"
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
            case "terminal":
                terminal = decodeTerminal(table, diagnostics: &diagnostics)
            default:
                preconditionFailure("Recognized configuration table is not decoded")
            }
        }

        return ParsedConfig(
            tables: tables,
            terminal: terminal,
            diagnostics: diagnostics
        )
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

    private static func decodeTerminal(
        _ table: TOMLTable,
        diagnostics: inout [ConfigDiagnostic]
    ) -> TerminalPreferences {
        var theme: String?
        var fontFamily: String?
        var fontSize: Float?
        var paddingX: Int?
        var paddingY: Int?

        for key in table.keys.sorted() {
            guard terminalKeys.contains(key) else {
                diagnostics.append(diagnostic(
                    "\(configurationKeyPath(table: "terminal", key: key)) is not recognized"
                ))
                continue
            }

            let path = "[terminal].\(key)"
            switch key {
            case "theme":
                do {
                    let value = try table.string(forKey: key)
                    guard TerminalPresentation.isKnownTheme(value) else {
                        diagnostics.append(diagnostic(
                            "[terminal].theme \"\(value)\" is not a known theme"
                        ))
                        continue
                    }
                    theme = value
                } catch {
                    diagnostics.append(diagnostic("\(path) must be a string"))
                }
            case "font_family":
                do {
                    let value = try table.string(forKey: key)
                        .trimmingCharacters(in: .whitespacesAndNewlines)
                    guard !value.isEmpty else {
                        diagnostics.append(diagnostic("\(path) must be a non-empty string"))
                        continue
                    }
                    fontFamily = value
                } catch {
                    diagnostics.append(diagnostic("\(path) must be a string"))
                }
            case "font_size":
                guard let value = number(forKey: key, in: table) else {
                    diagnostics.append(diagnostic("\(path) must be a number"))
                    continue
                }
                let converted = Float(value)
                guard value.isFinite, value > 0, converted.isFinite else {
                    diagnostics.append(diagnostic("\(path) must be finite and greater than zero"))
                    continue
                }
                fontSize = converted
            case "padding_x", "padding_y":
                do {
                    let rawValue = try table.integer(forKey: key)
                    guard rawValue >= 0, let value = Int(exactly: rawValue) else {
                        diagnostics.append(diagnostic("\(path) must be a non-negative integer"))
                        continue
                    }
                    if key == "padding_x" {
                        paddingX = value
                    } else {
                        paddingY = value
                    }
                } catch {
                    diagnostics.append(diagnostic("\(path) must be an integer"))
                }
            default:
                preconditionFailure("Recognized terminal key is not decoded")
            }
        }

        return TerminalPreferences(
            theme: theme,
            fontFamily: fontFamily,
            fontSize: fontSize,
            paddingX: paddingX,
            paddingY: paddingY
        )
    }

    private static func number(forKey key: String, in table: TOMLTable) -> Double? {
        if let value = try? table.integer(forKey: key) {
            return Double(value)
        }
        return try? table.float(forKey: key)
    }

    private static func diagnostic(_ message: String) -> ConfigDiagnostic {
        ConfigDiagnostic(severity: .error, message: message)
    }
}

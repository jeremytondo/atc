import Foundation

struct ResolvedKeymap: Sendable {
    indirect enum Node: Sendable {
        case command(CommandID)
        case prefix([KeyStroke: Node])
    }

    let root: [KeyStroke: Node]
    let menuShortcuts: [CommandID: KeyStroke]
    let generation: Int
}

enum Keymap {
    static let defaultLeader = "cmd+k"

    static let compiledDefaults: [(sequence: String, command: CommandID)] = [
        ("cmd+b", .toggleSidebar), ("leader>b", .toggleSidebar),
        ("cmd+shift+p", .toggleCommandPalette),
        ("cmd+d", .showDashboard), ("leader>d", .showDashboard),
        ("cmd+n", .newSession), ("leader>n", .newSession),
        ("cmd+r", .refresh), ("leader>r", .refresh),
        ("cmd+t", .newTerminal),
        ("cmd+shift+n", .newWorkspace),
        ("cmd+shift+s", .searchSessions), ("leader>s", .searchSessions),
        ("cmd+shift+t", .searchTerminals), ("leader>t", .searchTerminals),
        ("cmd+shift+o", .searchWorkspaces), ("leader>w", .searchWorkspaces),
    ]

    static func resolve(
        defaults: [(sequence: String, command: CommandID)] = compiledDefaults,
        user: ParsedConfig = .empty,
        generation: Int = 1
    ) -> Result<ResolvedKeymap, ConfigDiagnostics> {
        var diagnostics = user.diagnostics
        var leader = requiredStroke(defaultLeader)
        var clearDefaults = false

        for entry in user.tables["keyboard", default: []] {
            switch entry.key {
            case "leader":
                guard case .string(let text) = entry.value else {
                    diagnostics.append(.init(
                        severity: .error,
                        message: "[keyboard].leader must be a quoted trigger string"
                    ))
                    continue
                }
                switch KeyStroke.parse(text) {
                case .success(let stroke):
                    leader = stroke
                case .failure(let error):
                    diagnostics.append(.init(
                        severity: .error,
                        message: "[keyboard].leader has invalid trigger '\(text)': \(error.message)"
                    ))
                }
            case "clear_default_keybindings":
                guard case .boolean(let value) = entry.value else {
                    diagnostics.append(.init(
                        severity: .error,
                        message: "[keyboard].clear_default_keybindings must be a boolean"
                    ))
                    continue
                }
                clearDefaults = value
            default:
                continue
            }
        }

        struct OrderedEntry {
            let sequenceText: String
            let commandText: String
            let order: Int
        }
        var ordered: [OrderedEntry] = []
        if !clearDefaults {
            ordered = defaults.enumerated().map { offset, binding in
                OrderedEntry(
                    sequenceText: binding.sequence,
                    commandText: binding.command.rawValue,
                    order: offset
                )
            }
        }
        let userOffset = ordered.count
        for (offset, entry) in user.tables["keybindings", default: []].enumerated() {
            guard case .string(let command) = entry.value else {
                diagnostics.append(.init(
                    severity: .error,
                    message: "\(configurationKeyPath(table: "keybindings", key: entry.key)) must be a string command id or \"unbind\""
                ))
                continue
            }
            ordered.append(.init(
                sequenceText: entry.key,
                commandText: command,
                order: userOffset + offset
            ))
        }

        struct FoldedBinding {
            let command: CommandID
            let order: Int
        }
        var folded: [KeySequence: FoldedBinding] = [:]
        for entry in ordered {
            let parsed: ParsedKeySequence
            switch ParsedKeySequence.parse(entry.sequenceText) {
            case .success(let value): parsed = value
            case .failure(let error):
                diagnostics.append(.init(
                    severity: .error,
                    message: "\(configurationKeyPath(table: "keybindings", key: entry.sequenceText)) has an invalid trigger: \(error.message)"
                ))
                continue
            }
            let sequence: KeySequence
            switch parsed {
            case .direct(let stroke): sequence = [stroke]
            case .leader(let continuation): sequence = [leader, continuation]
            }

            if entry.commandText == "unbind" {
                folded.removeValue(forKey: sequence)
                continue
            }
            guard let command = CommandID(rawValue: entry.commandText) else {
                diagnostics.append(.init(
                    severity: .error,
                    message: "\(configurationKeyPath(table: "keybindings", key: entry.sequenceText)): unknown command id '\(entry.commandText)'"
                ))
                continue
            }
            folded[sequence] = FoldedBinding(
                command: command,
                order: entry.order
            )
        }

        // The step-one modifier rule (cmd/ctrl/option required) is enforced
        // when triggers parse: direct bindings and the leader go through
        // KeyStroke.parse, so every sequence[0] here already satisfies it.
        let directTriggers = Set(folded.keys.filter { $0.count == 1 }.map { $0[0] })
        let prefixTriggers = Set(folded.keys.filter { $0.count > 1 }.map { $0[0] })
        for trigger in directTriggers.intersection(prefixTriggers).sorted(by: descriptionOrder) {
            diagnostics.append(.init(
                severity: .error,
                message: "[keybindings] trigger '\(trigger)' is both a direct shortcut and a sequence prefix; unbind the direct shortcut or choose another leader"
            ))
        }

        let protected = protectedTriggers
        let protectedCandidates = directTriggers.union(prefixTriggers).union([leader])
        for trigger in protectedCandidates.sorted(by: descriptionOrder) {
            if let name = protected[trigger] {
                let source = trigger == leader
                    ? "[keyboard].leader '\(trigger)'"
                    : "[keybindings] trigger '\(trigger)'"
                diagnostics.append(.init(
                    severity: .error,
                    message: "\(source) is reserved for \(name)"
                ))
            }
        }

        guard !diagnostics.contains(where: { $0.severity == .error }) else {
            return .failure(ConfigDiagnostics(diagnostics))
        }

        var root: [KeyStroke: ResolvedKeymap.Node] = [:]
        for (sequence, binding) in folded {
            if sequence.count == 1 {
                root[sequence[0]] = .command(binding.command)
            } else {
                var continuations: [KeyStroke: ResolvedKeymap.Node]
                if case .prefix(let existing)? = root[sequence[0]] {
                    continuations = existing
                } else {
                    continuations = [:]
                }
                continuations[sequence[1]] = .command(binding.command)
                root[sequence[0]] = .prefix(continuations)
            }
        }

        var menuCandidates: [CommandID: (stroke: KeyStroke, order: Int)] = [:]
        for (sequence, binding) in folded where sequence.count == 1 {
            let candidate = menuCandidates[binding.command]
            if candidate == nil || binding.order > candidate!.order {
                menuCandidates[binding.command] = (sequence[0], binding.order)
            }
        }
        return .success(ResolvedKeymap(
            root: root,
            menuShortcuts: menuCandidates.mapValues(\.stroke),
            generation: generation
        ))
    }

    private static let protectedTriggers: [KeyStroke: String] = {
        let values: [(String, String)] = [
            ("cmd+q", "Quit"), ("cmd+h", "Hide atc"),
            ("cmd+opt+h", "Hide Others"), ("cmd+,", "Settings"),
            ("cmd+w", "Close Window"), ("cmd+shift+w", "Close All Windows"),
            ("cmd+z", "Undo"), ("cmd+shift+z", "Redo"),
            ("cmd+x", "Cut"), ("cmd+c", "Copy"), ("cmd+v", "Paste"),
            ("cmd+a", "Select All"), ("cmd+m", "Minimize"),
        ]
        return Dictionary(uniqueKeysWithValues: values.map { (requiredStroke($0.0), $0.1) })
    }()

    private static func requiredStroke(_ text: String) -> KeyStroke {
        switch KeyStroke.parse(text) {
        case .success(let stroke): stroke
        case .failure(let error): preconditionFailure("Invalid compiled trigger: \(error)")
        }
    }

    private static func descriptionOrder(_ lhs: KeyStroke, _ rhs: KeyStroke) -> Bool {
        lhs.description < rhs.description
    }
}

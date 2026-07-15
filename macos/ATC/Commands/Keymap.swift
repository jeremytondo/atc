import Foundation

struct ResolvedKeymap: Sendable {
    indirect enum Node: Sendable {
        case command(CommandID)
        case prefix([KeyStroke: Node])
    }

    let root: [KeyStroke: Node]
    let menuShortcuts: [CommandID: KeyStroke]
    let leaderTimeout: Duration
    let generation: Int
}

enum Keymap {
    static let defaultLeader = "cmd+k"
    static let defaultLeaderTimeoutMilliseconds = 1_800

    static let compiledDefaults: [(sequence: String, command: CommandID)] = [
        ("cmd+b", .toggleSidebar), ("leader>b", .toggleSidebar),
        ("cmd+n", .newSession), ("leader>n", .newSession),
        ("cmd+r", .refresh), ("leader>r", .refresh),
        ("cmd+t", .newTerminal),
        ("cmd+shift+n", .newWorkspace),
    ]

    static func resolve(
        defaults: [(sequence: String, command: CommandID)] = compiledDefaults,
        user: ParsedConfig = .empty,
        generation: Int = 1
    ) -> Result<ResolvedKeymap, ConfigDiagnostics> {
        var diagnostics = user.diagnostics
        var leader = requiredStroke(defaultLeader)
        var leaderLine: Int?
        var timeoutMilliseconds = defaultLeaderTimeoutMilliseconds
        var clearDefaults = false

        for entry in user.tables["keyboard", default: []] {
            switch entry.key {
            case "leader":
                guard case .string(let text) = entry.value else {
                    diagnostics.append(.init(
                        severity: .error,
                        line: entry.line,
                        message: "[keyboard].leader must be a quoted trigger string"
                    ))
                    continue
                }
                switch KeyStroke.parse(text) {
                case .success(let stroke):
                    leader = stroke
                    leaderLine = entry.line
                case .failure(let error):
                    diagnostics.append(.init(
                        severity: .error,
                        line: entry.line,
                        message: "Invalid leader '\(text)': \(error.message)"
                    ))
                }
            case "leader_timeout_ms":
                guard case .integer(let value) = entry.value, value > 0 else {
                    diagnostics.append(.init(
                        severity: .error,
                        line: entry.line,
                        message: "[keyboard].leader_timeout_ms must be a positive integer"
                    ))
                    continue
                }
                timeoutMilliseconds = value
            case "clear_default_keybindings":
                guard case .boolean(let value) = entry.value else {
                    diagnostics.append(.init(
                        severity: .error,
                        line: entry.line,
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
            let line: Int?
            let order: Int
        }
        var ordered: [OrderedEntry] = []
        if !clearDefaults {
            ordered = defaults.enumerated().map { offset, binding in
                OrderedEntry(
                    sequenceText: binding.sequence,
                    commandText: binding.command.rawValue,
                    line: nil,
                    order: offset
                )
            }
        }
        let userOffset = ordered.count
        for (offset, entry) in user.tables["keybindings", default: []].enumerated() {
            guard case .string(let command) = entry.value else {
                diagnostics.append(.init(
                    severity: .error,
                    line: entry.line,
                    message: "Binding '\(entry.key)' must map to a quoted command id or \"unbind\""
                ))
                continue
            }
            ordered.append(.init(
                sequenceText: entry.key,
                commandText: command,
                line: entry.line,
                order: userOffset + offset
            ))
        }

        struct FoldedBinding {
            let command: CommandID
            let order: Int
            let line: Int?
        }
        var folded: [KeySequence: FoldedBinding] = [:]
        for entry in ordered {
            let parsed: ParsedKeySequence
            switch ParsedKeySequence.parse(entry.sequenceText) {
            case .success(let value): parsed = value
            case .failure(let error):
                diagnostics.append(.init(
                    severity: .error,
                    line: entry.line,
                    message: "Invalid binding trigger '\(entry.sequenceText)': \(error.message)"
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
                    line: entry.line,
                    message: "Unknown command id '\(entry.commandText)' for '\(entry.sequenceText)'"
                ))
                continue
            }
            folded[sequence] = FoldedBinding(
                command: command,
                order: entry.order,
                line: entry.line
            )
        }

        for (sequence, binding) in folded where !sequence[0].hasPrimaryModifier {
            diagnostics.append(.init(
                severity: .error,
                line: binding.line,
                message: "Step-one trigger '\(sequence[0])' must include cmd, ctrl, or option"
            ))
        }

        let directTriggers = Set(folded.keys.filter { $0.count == 1 }.map { $0[0] })
        let prefixTriggers = Set(folded.keys.filter { $0.count > 1 }.map { $0[0] })
        func sourceLine(for trigger: KeyStroke) -> Int? {
            if trigger == leader, let leaderLine { return leaderLine }
            return folded.first { sequence, _ in sequence[0] == trigger }?.value.line
        }
        for trigger in directTriggers.intersection(prefixTriggers).sorted(by: descriptionOrder) {
            diagnostics.append(.init(
                severity: .error,
                line: sourceLine(for: trigger),
                message: "Trigger '\(trigger)' is both a direct shortcut and a sequence prefix; unbind the direct shortcut or choose another leader"
            ))
        }

        let protected = protectedTriggers
        let protectedCandidates = directTriggers.union(prefixTriggers).union([leader])
        for trigger in protectedCandidates.sorted(by: descriptionOrder) {
            if let name = protected[trigger] {
                diagnostics.append(.init(
                    severity: .error,
                    line: sourceLine(for: trigger),
                    message: "\(trigger) is reserved for \(name)"
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
            leaderTimeout: .milliseconds(timeoutMilliseconds),
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

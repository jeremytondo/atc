import Foundation

// The single registry for native shortcuts protected from configuration and
// for the subset intercepted before a focused terminal can claim them.
nonisolated enum AppAction: Equatable, Sendable {
    case terminate
    case closeWindow
    case closeAllWindows
}

nonisolated enum NativeShortcuts {
    private enum Owner: Sendable {
        case appHandled(AppAction)
        case responderOwned
    }

    private struct Entry: Sendable {
        let name: String
        let owner: Owner
    }

    private static let entries: [KeyStroke: Entry] = {
        let values: [(String, String, Owner)] = [
            ("cmd+q", "Quit", .appHandled(.terminate)),
            ("cmd+h", "Hide atc", .responderOwned),
            ("cmd+opt+h", "Hide Others", .responderOwned),
            ("cmd+,", "Settings", .responderOwned),
            ("cmd+w", "Close Window", .appHandled(.closeWindow)),
            ("cmd+shift+w", "Close All Windows", .appHandled(.closeAllWindows)),
            ("cmd+z", "Undo", .responderOwned),
            ("cmd+shift+z", "Redo", .responderOwned),
            ("cmd+x", "Cut", .responderOwned),
            ("cmd+c", "Copy", .responderOwned),
            ("cmd+v", "Paste", .responderOwned),
            ("cmd+a", "Select All", .responderOwned),
            ("cmd+m", "Minimize", .responderOwned),
        ]
        return Dictionary(uniqueKeysWithValues: values.map {
            (requiredStroke($0.0), Entry(name: $0.1, owner: $0.2))
        })
    }()

    static let protectedTriggers: [KeyStroke: String] = entries.mapValues(\.name)

    static func appAction(for stroke: KeyStroke) -> AppAction? {
        guard case .appHandled(let action)? = entries[stroke]?.owner else { return nil }
        return action
    }

    private static func requiredStroke(_ text: String) -> KeyStroke {
        switch KeyStroke.parse(text) {
        case .success(let stroke): stroke
        case .failure(let error): preconditionFailure("Invalid compiled trigger: \(error)")
        }
    }
}

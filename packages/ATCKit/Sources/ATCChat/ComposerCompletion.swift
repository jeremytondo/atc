// The composer's completions (ATC-216): `@` mentions a file, `/` names a
// command. Both are plain text in the end — an accepted completion replaces
// the trigger and what was typed after it with the chosen text and a space;
// nothing structured survives into the prompt. This type is the pure part:
// where a trigger is, what an acceptance produces, how commands filter.
// Views own the fetching and the list.
//
//   - `@` triggers at a word start (the start of the text, or after
//     whitespace) and stays active while the caret follows it with no
//     whitespace in between — `foo @src/ma|` is a file query "src/ma",
//     `mail@example|` is not a trigger.
//   - `/` triggers only at the very start of the text, so a path typed
//     mid-sentence never opens the command list.

import ATCAppServerAPI
import Foundation

public nonisolated enum ComposerCompletion {
    public enum Kind: Equatable, Sendable {
        case file
        case command
    }

    /// An active trigger: what was typed after it, and the span an
    /// acceptance replaces (the trigger character through the caret).
    public struct Trigger: Equatable, Sendable {
        public let kind: Kind
        public let query: String
        public let range: Range<String.Index>

        public init(kind: Kind, query: String, range: Range<String.Index>) {
            self.kind = kind
            self.query = query
            self.range = range
        }
    }

    /// The trigger the caret sits in, if any (see the header's rules).
    public static func trigger(in text: String, caret: String.Index) -> Trigger? {
        guard caret <= text.endIndex else { return nil }
        // Walk back from the caret to the start of the current word.
        var start = caret
        while start > text.startIndex {
            let previous = text.index(before: start)
            if text[previous].isWhitespace || text[previous].isNewline { break }
            start = previous
        }
        guard start < caret else { return nil }
        let word = text[start..<caret]
        let query = String(word.dropFirst())
        switch word.first {
        case "@":
            return Trigger(kind: .file, query: query, range: start..<caret)
        case "/" where start == text.startIndex:
            return Trigger(kind: .command, query: query, range: start..<caret)
        default:
            return nil
        }
    }

    /// `text` with the trigger's span replaced by `replacement` plus a
    /// space, and the caret just after it.
    public static func accept(
        _ trigger: Trigger, in text: String, with replacement: String
    ) -> (text: String, caret: String.Index) {
        let inserted = replacement + " "
        var result = text
        result.replaceSubrange(trigger.range, with: inserted)
        let caret = result.index(trigger.range.lowerBound, offsetBy: inserted.count)
        return (result, caret)
    }

    /// Commands matching `query` (case-insensitive): name-prefix matches
    /// first, then the rest in the provider's order; everything when empty.
    public static func filter(_ commands: [AgentCommand], query: String) -> [AgentCommand] {
        let needle = query.lowercased()
        guard !needle.isEmpty else { return commands }
        let prefixed = commands.filter { $0.name.lowercased().hasPrefix(needle) }
        let rest = commands.filter { command in
            !command.name.lowercased().hasPrefix(needle)
                && (command.name.lowercased().contains(needle)
                    || command.description.lowercased().contains(needle))
        }
        return prefixed + rest
    }
}

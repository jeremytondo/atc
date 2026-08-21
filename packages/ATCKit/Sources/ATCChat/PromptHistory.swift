// The ⌘↑/⌘↓ walk through a thread's previous prompts (ATC-216): newest
// first, from the draft the walk started on, and back to it. The prompts
// are the caller's (the chat model projects them from the transcript); the
// walk keeps the list it started on and ends the moment the caller's list
// differs (a prompt landed, a resync), rather than step through shifted
// indexes. Any edit to the shown text ends the walk too (`reset`), so
// typing never fights the history.

public nonisolated struct PromptHistory: Equatable, Sendable {
    /// Position in the prompts while walking; nil at the draft.
    public private(set) var position: Int?
    /// The list the walk started on; a different list ends the walk.
    private var walked: [String] = []
    /// The draft the walk left, restored when walking back past the newest.
    private var draft = ""
    /// The text the last step produced — what the composer shows if the
    /// user has not typed since.
    public private(set) var shown: String?

    public init() {}

    /// One step toward older prompts from `current` (the composer text)
    /// through `prompts` (newest first); nil when there is nothing older,
    /// or when the list changed under a walk (which ends it).
    public mutating func older(from current: String, in prompts: [String]) -> String? {
        guard let position else {
            guard let first = prompts.first else { return nil }
            draft = current
            walked = prompts
            self.position = 0
            shown = first
            return first
        }
        guard prompts == walked else {
            reset()
            return nil
        }
        guard position + 1 < walked.count else { return nil }
        self.position = position + 1
        shown = walked[position + 1]
        return shown
    }

    /// One step back toward the draft; nil when already at the draft, or
    /// when the list changed under the walk (which ends it).
    public mutating func newer(in prompts: [String]) -> String? {
        guard let position else { return nil }
        guard prompts == walked else {
            reset()
            return nil
        }
        if position == 0 {
            self.position = nil
            shown = draft
            return draft
        }
        self.position = position - 1
        shown = walked[position - 1]
        return shown
    }

    /// The user typed: the walk is over, the text is theirs.
    public mutating func reset() {
        position = nil
        shown = nil
    }
}

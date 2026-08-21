import ATCAppServerAPI
import Foundation
import Testing

@testable import ATCChat

/// The composer's pure completion rules: where `@` and `/` trigger, what an
/// acceptance produces, how commands filter — and the ⌘↑/⌘↓ history walk.
@Suite("ComposerCompletion")
struct ComposerCompletionTests {
    private func trigger(_ text: String, caretOffset: Int? = nil) -> ComposerCompletion.Trigger? {
        let caret = caretOffset.map { text.index(text.startIndex, offsetBy: $0) } ?? text.endIndex
        return ComposerCompletion.trigger(in: text, caret: caret)
    }

    @Test("@ triggers at a word start and carries what follows it")
    func atTriggers() {
        #expect(trigger("@")?.kind == .file)
        #expect(trigger("@src/ma")?.query == "src/ma")
        #expect(trigger("look at @src/ma")?.query == "src/ma")
        #expect(trigger("line one\n@ma")?.query == "ma")
        // Mid-word, or with whitespace after it, `@` is just text.
        #expect(trigger("mail@example") == nil)
        #expect(trigger("@src/ma done") == nil)
        #expect(trigger("plain words") == nil)
    }

    @Test("/ triggers only at the very start of the text")
    func slashTriggers() {
        #expect(trigger("/")?.kind == .command)
        #expect(trigger("/rev")?.query == "rev")
        #expect(trigger("see /usr/bin") == nil)
        #expect(trigger("/review now") == nil)
    }

    @Test("the trigger follows the caret, not the end of the text")
    func caretAware() {
        let text = "@src tail"
        #expect(trigger(text, caretOffset: 4)?.query == "src")
        #expect(trigger(text, caretOffset: 9) == nil)
    }

    @Test("accepting replaces the trigger span with the choice and a space, caret after")
    func accept() throws {
        let text = "look at @src/ma please"
        let caret = text.index(text.startIndex, offsetBy: 15)
        let found = try #require(ComposerCompletion.trigger(in: text, caret: caret))
        let accepted = ComposerCompletion.accept(found, in: text, with: "@src/main.swift")
        #expect(accepted.text == "look at @src/main.swift  please")
        #expect(accepted.text[..<accepted.caret] == "look at @src/main.swift ")
    }

    @Test("commands filter by name prefix first, then by name or description substring")
    func filter() {
        let commands: [AgentCommand] = [
            .init(name: "commit", description: "Commit staged changes"),
            .init(name: "review", description: "Review the diff"),
            .init(name: "pr-review", description: "Open a pull request review"),
        ]
        #expect(ComposerCompletion.filter(commands, query: "").map(\.name) == ["commit", "review", "pr-review"])
        #expect(ComposerCompletion.filter(commands, query: "rev").map(\.name) == ["review", "pr-review"])
        #expect(ComposerCompletion.filter(commands, query: "staged").map(\.name) == ["commit"])
        #expect(ComposerCompletion.filter(commands, query: "zzz").isEmpty)
    }

    @Test("the history walk goes newest-first from the draft and back to it; an edit ends it")
    func history() {
        let prompts = ["third", "second", "first"]
        var history = PromptHistory()
        #expect(history.newer(in: prompts) == nil)
        #expect(history.older(from: "draft", in: prompts) == "third")
        #expect(history.older(from: "third", in: prompts) == "second")
        #expect(history.older(from: "second", in: prompts) == "first")
        #expect(history.older(from: "first", in: prompts) == nil)
        #expect(history.newer(in: prompts) == "second")
        #expect(history.newer(in: prompts) == "third")
        #expect(history.newer(in: prompts) == "draft")
        #expect(history.newer(in: prompts) == nil)
        // A walk in progress that the user edits is over: the next ⌘↑
        // starts again from the edited text.
        #expect(history.older(from: "draft", in: prompts) == "third")
        history.reset()
        #expect(history.shown == nil)
        #expect(history.older(from: "third edited", in: prompts) == "third")
        #expect(history.newer(in: prompts) == "third edited")
        // The list changes under a walk (a prompt landed, a resync): the
        // walk ends instead of stepping through shifted indexes.
        #expect(history.older(from: "draft", in: prompts) == "third")
        #expect(history.older(from: "third", in: prompts) == "second")
        #expect(history.newer(in: ["fourth"] + prompts) == nil)
        #expect(history.position == nil)
        #expect(history.older(from: "draft", in: ["only"]) == "only")
        #expect(history.older(from: "only", in: []) == nil)
        #expect(history.position == nil)
    }
}

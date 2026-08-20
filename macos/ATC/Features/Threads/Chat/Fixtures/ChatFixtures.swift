// Recorded real transcripts (ATC-214): one Claude Code thread and one Codex
// thread, captured verbatim from `GET /transcript` on a live server. They
// drive the Chat `#Preview`s and the layout tests, so rendering work is
// reviewable without a live server. Re-record by piping
// `atc thread transcript <id>` over a fixture file — never hand-edit the
// JSON, it is a wire sample.

import ATCAppServerAPI
import Foundation

enum ChatFixtures {
    /// A real Claude Code thread: user prompts, streamed answers, reasoning,
    /// commands, and interrupted turns across 22 turns.
    static var claude: ThreadTranscriptPage { load("chat-fixture-claude") }

    /// A real Codex thread: one turn with provider metadata on the answer.
    static var codex: ThreadTranscriptPage { load("chat-fixture-codex") }

    private static func load(_ name: String) -> ThreadTranscriptPage {
        guard let url = Bundle(for: ChatItemModel.self).url(forResource: name, withExtension: "json")
        else { fatalError("chat fixture \(name).json is not in the app bundle") }
        do {
            return try makeJSONDecoder().decode(ThreadTranscriptPage.self, from: Data(contentsOf: url))
        } catch {
            fatalError("chat fixture \(name).json no longer decodes: \(error)")
        }
    }
}

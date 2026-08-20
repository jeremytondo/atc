// Chat previews: the recorded fixtures (ChatFixtures) exactly as
// ChatTranscriptView lays them out, plus a built gallery covering the item
// kinds and turn states the recorded threads don't happen to contain —
// markdown-rich answers, file changes, MCP/tool calls, compaction, failed
// and interrupted turns, and an inline approval. The gallery items are
// constructed in code: the fixture JSONs are wire samples and stay
// unedited. This is where rendering work is reviewed without a server.

import ATCAppServerAPI
import ATCDesign
import OpenAPIRuntime
import SwiftUI

#if DEBUG
    private struct ChatRowsPreview: View {
        let rows: [ChatRowModel]

        init(page: ThreadTranscriptPage, requests: [ThreadRequest] = []) {
            var transcript = ChatTranscript()
            transcript.load(page)
            transcript.replaceRequests(requests)
            var boxes: [String: ChatItemModel] = [:]
            var folds: [String: ChatWorkModel] = [:]
            rows = ChatRowBuilder.rows(from: transcript, reusing: &boxes, folds: &folds).rows
        }

        var body: some View {
            ScrollView {
                LazyVStack(alignment: .leading, spacing: Spacing.md) {
                    ForEach(rows) { row in
                        ChatRowView(row: row)
                    }
                }
                .padding(Spacing.xxl)
                .frame(maxWidth: 820)
                .frame(maxWidth: .infinity)
            }
            .background(AppColors.canvas)
        }
    }

    /// Builders for the gallery: every item kind, in one settled turn, one
    /// failed turn, and one running turn with a blocked approval.
    private enum Gallery {
        static let markdown = """
            ## What changed

            The parser now keeps **settled blocks** stable while streaming — only
            the *trailing* block re-parses. Inline `code`, [links](https://example.com),
            and ~~mistakes~~ render natively.

            ```swift
            let blocks = MarkdownParser.blocks(from: text)
            ```

            > Block quotes render as quotes, not as `>` noise.

            1. Ordered lists
            2. With real markers

            | column | value |
            | ------ | ----- |
            | blocks | 7     |
            """

        static var page: ThreadTranscriptPage {
            ThreadTranscriptPage(
                items: [
                    .userMessage(
                        .init(
                            _type: .userMessage, id: "u1", turnId: "t1",
                            createdAt: date(0), text: "Render **markdown** properly, please.")),
                    .reasoning(
                        .init(
                            _type: .reasoning, id: "r1", turnId: "t1",
                            text: "The transcript needs block rendering; planning the parse.",
                            complete: true)),
                    .command(
                        .init(
                            _type: .command, id: "c1", turnId: "t1", title: "bun test",
                            status: .completed, command: "bun test", cwd: "~/atc",
                            output: "42 pass\n0 fail", exitCode: 0)),
                    .fileChange(
                        .init(
                            _type: .fileChange, id: "f1", turnId: "t1", title: "Edit MarkdownBlocks.swift",
                            status: .completed,
                            changes: [
                                Components.Schemas.FileChange(
                                    path: "Sources/ATCChat/MarkdownBlocks.swift", kind: .update,
                                    diff: "@@ -1,3 +1,4 @@\n+import Markdown")
                            ])),
                    .mcpCall(
                        .init(
                            _type: .mcpCall, id: "m1", turnId: "t1", title: "linear get_issue",
                            status: .completed, server: "linear", tool: "get_issue",
                            arguments: container(["id": "ATC-215"]),
                            result: container(["title": "Render the conversation"]))),
                    .toolCall(
                        .init(
                            _type: .toolCall, id: "tc1", turnId: "t1", title: "Read Package.swift",
                            status: .error, error: "file not found", name: "Read",
                            input: container(["path": "Package.swift"]))),
                    .assistantText(
                        .init(
                            _type: .assistantText, id: "a1", turnId: "t1",
                            createdAt: date(72), text: markdown, complete: true)),
                    .compaction(
                        .init(_type: .compaction, id: "k1", turnId: "t2")),
                    .userMessage(
                        .init(
                            _type: .userMessage, id: "u2", turnId: "t3",
                            createdAt: date(120), text: "Now break, so I can see the marker.")),
                    .command(
                        .init(
                            _type: .command, id: "c2", turnId: "t3", title: "bun run flaky",
                            status: .error, command: "bun run flaky", exitCode: 1)),
                    .userMessage(
                        .init(
                            _type: .userMessage, id: "u3", turnId: "t4",
                            createdAt: date(200), text: "And one turn that is still running.")),
                    .reasoning(
                        .init(
                            _type: .reasoning, id: "r2", turnId: "t4",
                            text: "Streaming thoughts…", complete: false)),
                    .command(
                        .init(
                            _type: .command, id: "c3", turnId: "t4", title: "rm -rf node_modules",
                            status: .pending, command: "rm -rf node_modules")),
                ],
                turns: [
                    ThreadTurn(id: "t1", status: .completed, startedAt: date(0), endedAt: date(72)),
                    ThreadTurn(id: "t2", status: .completed, startedAt: date(80), endedAt: date(81)),
                    ThreadTurn(
                        id: "t3", status: .failed, error: "the command failed", startedAt: date(120), endedAt: date(125)
                    ),
                    ThreadTurn(id: "t4", status: .running, startedAt: date(200)),
                ],
                seq: 1, snapshotVersion: 1, hasMore: false
            )
        }

        /// An approval blocked on the running turn's pending command.
        static var approval: ThreadRequest {
            .approval(
                .init(
                    kind: .approval, id: "req1", turnId: "t4", itemId: "c3", openedAt: date(201),
                    title: "Delete node_modules?", reason: "The command removes files.",
                    subject: .command(.init(_type: .command, command: "rm -rf node_modules", cwd: "~/atc"))))
        }

        private static func date(_ seconds: TimeInterval) -> Date {
            Date(timeIntervalSince1970: 1_755_000_000 + seconds)
        }

        // A [String: String] payload always validates; previews only.
        private static func container(_ value: [String: String]) -> OpenAPIValueContainer {
            guard let container = try? OpenAPIValueContainer(unvalidatedValue: value) else {
                fatalError("preview payload failed to validate")
            }
            return container
        }
    }

    #Preview("Claude transcript") {
        ChatRowsPreview(page: ChatFixtures.claude)
            .frame(width: 760, height: 900)
    }

    #Preview("Codex transcript") {
        ChatRowsPreview(page: ChatFixtures.codex)
            .frame(width: 760, height: 500)
    }

    #Preview("Every item kind") {
        ChatRowsPreview(page: Gallery.page, requests: [Gallery.approval])
            .frame(width: 760, height: 1100)
    }
#endif

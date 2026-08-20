import Foundation
import Testing

@testable import ATCChat

/// The markdown block model: what each construct parses into, and the
/// streaming cache's contract — appending re-parses only the trailing
/// top-level block and never changes what earlier blocks parsed to.
@Suite("Markdown blocks")
struct MarkdownBlocksTests {
    @Test("every block construct parses to its native block")
    func blockKinds() {
        let blocks = MarkdownParser.blocks(
            from: """
                # Title

                A paragraph.

                ```swift
                let x = 1
                ```

                > quoted words

                1. first
                2. second

                - [x] done
                - [ ] open

                | a | b |
                | - | - |
                | 1 | 2 |

                ---
                """)
        guard blocks.count == 8 else {
            Issue.record("expected 8 blocks, got \(blocks)")
            return
        }
        guard case .heading(_, 1, let title) = blocks[0] else {
            Issue.record("no heading")
            return
        }
        #expect(String(title.characters) == "Title")
        guard case .paragraph = blocks[1] else {
            Issue.record("no paragraph")
            return
        }
        guard case .code(_, "swift", "let x = 1") = blocks[2] else {
            Issue.record("no code: \(blocks[2])")
            return
        }
        guard case .quote(_, let quoted) = blocks[3], case .paragraph = quoted.first else {
            Issue.record("no quote")
            return
        }
        guard case .list(_, let ordered) = blocks[4] else {
            Issue.record("no ordered list")
            return
        }
        #expect(ordered.map(\.marker) == ["1.", "2."])
        guard case .list(_, let tasks) = blocks[5] else {
            Issue.record("no task list")
            return
        }
        #expect(tasks.map(\.marker) == ["☑", "☐"])
        guard case .table(_, let header, let rows) = blocks[6] else {
            Issue.record("no table: \(blocks[6])")
            return
        }
        #expect(header.map { String($0.characters) } == ["a", "b"])
        #expect(rows.map { $0.map { String($0.characters) } } == [["1", "2"]])
        guard case .divider = blocks[7] else {
            Issue.record("no divider")
            return
        }
    }

    @Test("inline emphasis, code, links, and strikethrough carry their intents")
    func inlineRuns() {
        let blocks = MarkdownParser.blocks(from: "**bold** *italic* `code` ~~gone~~ [site](https://example.com)")
        guard case .paragraph(_, let text) = blocks.first else {
            Issue.record("no paragraph")
            return
        }
        func run(containing needle: String) -> AttributedString.Runs.Run? {
            text.runs.first { String(text[$0.range].characters).contains(needle) }
        }
        #expect(run(containing: "bold")?.inlinePresentationIntent == .stronglyEmphasized)
        #expect(run(containing: "italic")?.inlinePresentationIntent == .emphasized)
        #expect(run(containing: "code")?.inlinePresentationIntent == .code)
        #expect(run(containing: "gone")?.inlinePresentationIntent == .strikethrough)
        #expect(run(containing: "site")?.link == URL(string: "https://example.com"))
    }

    @Test("streaming appends match a full parse at every step and never rewrite settled blocks")
    func streamingMatchesFullParse() {
        let full = """
            # Title

            First paragraph grows here.

            ```swift
            let a = 1
            let b = 2
            ```

            - item one
            - item two

            Closing prose.
            """
        var cache = MarkdownStreamCache()
        var settled: [Int: MarkdownBlock] = [:]
        var text = ""
        for character in full {
            text.append(character)
            let streamed = cache.update(text)
            #expect(streamed == MarkdownParser.blocks(from: text))
            // A block that stopped being the last one is settled: it must
            // never change again on later appends.
            for block in streamed.dropLast() {
                if let seen = settled[block.id] {
                    #expect(seen == block)
                } else {
                    settled[block.id] = block
                }
            }
        }
    }

    @Test("an open fence streams as one code block until it closes")
    func openFenceStreams() {
        var cache = MarkdownStreamCache()
        _ = cache.update("intro\n\n```swift\nlet x")
        let open = cache.update("intro\n\n```swift\nlet x = 1\n")
        guard case .code(_, "swift", let streaming) = open.last else {
            Issue.record("open fence should already render as code: \(open)")
            return
        }
        #expect(streaming.contains("let x = 1"))
        let closed = cache.update("intro\n\n```swift\nlet x = 1\n```\n\ndone")
        guard case .code(_, "swift", "let x = 1") = closed[1] else {
            Issue.record("closed fence lost its code: \(closed)")
            return
        }
        guard case .paragraph = closed.last else {
            Issue.record("trailing prose missing")
            return
        }
    }

    @Test("a reference definition anywhere keeps streaming equal to a full parse")
    func referenceDefinitionResolves() {
        // Def arriving late (behind the settled boundary) and def settling
        // early with the reference streaming in afterwards.
        for full in [
            "See [docs][d].\n\nplaceholder\n\n[d]: https://example.com",
            "[d]: https://example.com\n\nSome intro paragraph.\n\nSee [d] for details.",
        ] {
            var cache = MarkdownStreamCache()
            var text = ""
            for character in full {
                text.append(character)
                #expect(cache.update(text) == MarkdownParser.blocks(from: text))
            }
            let linked = cache.update(full).contains { block in
                guard case .paragraph(_, let paragraph) = block else { return false }
                return paragraph.runs.contains { $0.link == URL(string: "https://example.com") }
            }
            #expect(linked)
        }
    }

    @Test("a non-append update falls back to a clean full parse")
    func nonAppendFallsBack() {
        var cache = MarkdownStreamCache()
        _ = cache.update("one\n\ntwo\n\nthree")
        let replaced = cache.update("entirely different")
        #expect(replaced == MarkdownParser.blocks(from: "entirely different"))
    }
}

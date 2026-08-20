// The block model Chat renders markdown through, parsed with Apple's
// swift-markdown (no third-party markdown view — layout, selection, and
// streaming stay ours). Inline runs become `AttributedString` presentation
// intents that SwiftUI `Text` renders natively; block structure becomes
// `MarkdownBlock` values the views lay out.
//
// Streaming: `MarkdownStreamCache` re-parses only the trailing top-level
// block. The parsed source up to the last block's start is stable — markdown
// prior to the last top-level block cannot be changed by appending text —
// with one exception: a link reference definition applies document-wide, so
// a text that contains one anywhere always takes the full parse (as does
// any update that is not a pure append). Every other delta re-parses just
// the tail and the earlier blocks (and their ids) are reused as-is.

import Foundation
import Markdown

/// One rendered markdown block. `id` is the block's position in its list —
/// stable under streaming because only the trailing block ever changes.
enum MarkdownBlock: Identifiable, Equatable {
    case paragraph(id: Int, text: AttributedString)
    case heading(id: Int, level: Int, text: AttributedString)
    case code(id: Int, language: String?, code: String)
    case quote(id: Int, blocks: [MarkdownBlock])
    case list(id: Int, items: [MarkdownListItem])
    case table(id: Int, header: [AttributedString], rows: [[AttributedString]])
    case divider(id: Int)

    var id: Int {
        switch self {
        case .paragraph(let id, _), .heading(let id, _, _), .code(let id, _, _),
            .quote(let id, _), .list(let id, _), .table(let id, _, _), .divider(let id):
            id
        }
    }
}

/// One list item: its marker ("•", "1.", "☐"/"☑" for task items) and its
/// nested blocks.
struct MarkdownListItem: Identifiable, Equatable {
    let id: Int
    let marker: String
    let blocks: [MarkdownBlock]
}

enum MarkdownParser {
    /// The whole text as blocks (a non-streaming, one-shot parse).
    static func blocks(from text: String) -> [MarkdownBlock] {
        blocks(of: Document(parsing: text))
    }

    static func blocks(of markup: Markup) -> [MarkdownBlock] {
        var blocks: [MarkdownBlock] = []
        for child in markup.children {
            appendBlock(child, to: &blocks)
        }
        return blocks
    }

    /// One markup child as a block. HTML blocks render as code; anything
    /// unknown degrades to its plain source text rather than disappearing.
    private static func appendBlock(_ markup: Markup, to blocks: inout [MarkdownBlock]) {
        let id = blocks.count
        switch markup {
        case let paragraph as Paragraph:
            blocks.append(.paragraph(id: id, text: inlineText(of: paragraph)))
        case let heading as Heading:
            blocks.append(.heading(id: id, level: heading.level, text: inlineText(of: heading)))
        case let code as CodeBlock:
            let language = code.language?.trimmingCharacters(in: .whitespaces)
            blocks.append(
                .code(
                    id: id,
                    language: (language?.isEmpty ?? true) ? nil : language,
                    // Fenced code carries a trailing newline; the view adds
                    // its own spacing.
                    code: String(code.code.trimmingSuffix(while: \.isNewline))
                ))
        case let quote as BlockQuote:
            blocks.append(.quote(id: id, blocks: self.blocks(of: quote)))
        case let list as UnorderedList:
            blocks.append(.list(id: id, items: items(of: list, marker: { _ in "•" })))
        case let list as OrderedList:
            let start = Int(list.startIndex)
            blocks.append(.list(id: id, items: items(of: list, marker: { "\(start + $0)." })))
        case let table as Markdown.Table:
            blocks.append(
                .table(
                    id: id,
                    header: table.head.cells.map(inlineText(of:)),
                    rows: table.body.rows.map { $0.cells.map(inlineText(of:)) }
                ))
        case is ThematicBreak:
            blocks.append(.divider(id: id))
        case let html as HTMLBlock:
            blocks.append(.code(id: id, language: "html", code: html.rawHTML))
        default:
            // Anything unexpected renders as its plain source text.
            let text = markup.format().trimmingCharacters(in: .whitespacesAndNewlines)
            if !text.isEmpty {
                blocks.append(.paragraph(id: id, text: AttributedString(text)))
            }
        }
    }

    private static func items(
        of list: some ListItemContainer, marker: (Int) -> String
    ) -> [MarkdownListItem] {
        list.listItems.enumerated().map { index, item in
            MarkdownListItem(
                id: index,
                marker: item.checkbox.map { $0 == .checked ? "☑" : "☐" } ?? marker(index),
                blocks: blocks(of: item)
            )
        }
    }

    // MARK: - Inline runs

    /// The concatenated inline runs of a container (paragraph, heading,
    /// table cell) as presentation-intent attributed text.
    static func inlineText(of markup: Markup) -> AttributedString {
        var text = AttributedString()
        for child in markup.children {
            append(child, to: &text, intent: nil, link: nil)
        }
        return text
    }

    private static func append(
        _ markup: Markup, to text: inout AttributedString,
        intent: InlinePresentationIntent?, link: URL?
    ) {
        func merged(_ addition: InlinePresentationIntent) -> InlinePresentationIntent {
            var merged = intent ?? []
            merged.insert(addition)
            return merged
        }
        func appendRun(_ string: String) {
            var run = AttributedString(string)
            if let intent { run.inlinePresentationIntent = intent }
            if let link { run.link = link }
            text.append(run)
        }
        switch markup {
        case let plain as Markdown.Text:
            appendRun(plain.string)
        case is SoftBreak:
            appendRun(" ")
        case is LineBreak:
            appendRun("\n")
        case let code as InlineCode:
            var run = AttributedString(code.code)
            run.inlinePresentationIntent = merged(.code)
            if let link { run.link = link }
            text.append(run)
        case let html as InlineHTML:
            appendRun(html.rawHTML)
        case let symbol as SymbolLink:
            appendRun(symbol.destination ?? "")
        case let image as Image:
            // No inline image loading: the alt text stands in, linked when
            // the source parses as a URL.
            let destination = image.source.flatMap(URL.init(string:))
            for child in image.children {
                append(child, to: &text, intent: intent, link: destination ?? link)
            }
        case let emphasis as Emphasis:
            for child in emphasis.children {
                append(child, to: &text, intent: merged(.emphasized), link: link)
            }
        case let strong as Strong:
            for child in strong.children {
                append(child, to: &text, intent: merged(.stronglyEmphasized), link: link)
            }
        case let strike as Strikethrough:
            for child in strike.children {
                append(child, to: &text, intent: merged(.strikethrough), link: link)
            }
        case let anchor as Markdown.Link:
            let destination = anchor.destination.flatMap(URL.init(string:))
            for child in anchor.children {
                append(child, to: &text, intent: intent, link: destination ?? link)
            }
        default:
            for child in markup.children {
                append(child, to: &text, intent: intent, link: link)
            }
        }
    }
}

/// The streaming parse memo: one per streaming text item. Appending text
/// re-parses only from the start of the previously last top-level block;
/// everything before it is reused, ids included.
struct MarkdownStreamCache {
    private var text = ""
    private var blocks: [MarkdownBlock] = []
    /// UTF-8 offset where the last top-level block's source starts; the
    /// text before it is settled.
    private var tailStart = 0
    /// Blocks strictly before `tailStart`.
    private var settled: [MarkdownBlock] = []

    mutating func update(_ newText: String) -> [MarkdownBlock] {
        guard newText != text else { return blocks }
        let newUTF8 = Array(newText.utf8)
        if tailStart > 0, newUTF8.count >= tailStart,
            newUTF8[..<tailStart].elementsEqual(text.utf8.prefix(tailStart)),
            let tail = String(bytes: newUTF8[tailStart...], encoding: .utf8),
            !Self.holdsReferenceDefinition(newText)
        {
            let document = Document(parsing: tail)
            blocks = settled + MarkdownParser.blocks(of: document).map { $0.renumbered(by: settled.count) }
            advance(document: document, base: tailStart, utf8: newUTF8)
        } else {
            let document = Document(parsing: newText)
            blocks = MarkdownParser.blocks(of: document)
            settled = []
            tailStart = 0
            advance(document: document, base: 0, utf8: newUTF8)
        }
        text = newText
        return blocks
    }

    /// A link reference definition ("[name]: url") rewrites references
    /// anywhere in the document — including ones already settled — so a
    /// text carrying one anywhere is never parsed piecewise.
    private static func holdsReferenceDefinition(_ text: String) -> Bool {
        text.contains(/(?m)^ {0,3}\[[^\]\n]+\]:/)
    }

    /// Moves the settled boundary to the last top-level block's source start
    /// (top-level blocks begin at column 1, so a line offset suffices).
    private mutating func advance(document: Document, base: Int, utf8: [UInt8]) {
        guard let lastLine = document.children.reversed().compactMap({ $0.range?.lowerBound.line }).first,
            lastLine > 1
        else {
            // Zero or one block in the parsed slice: the boundary stays.
            return
        }
        var line = 1
        var offset = base
        for index in base..<utf8.count where utf8[index] == UInt8(ascii: "\n") {
            line += 1
            if line == lastLine {
                offset = index + 1
                break
            }
        }
        guard offset > tailStart else { return }
        tailStart = offset
        settled = Array(blocks.dropLast())
    }
}

extension MarkdownBlock {
    /// The same block with its id shifted by `offset` (tail blocks appended
    /// after reused settled blocks).
    fileprivate func renumbered(by offset: Int) -> MarkdownBlock {
        switch self {
        case .paragraph(let id, let text): .paragraph(id: id + offset, text: text)
        case .heading(let id, let level, let text): .heading(id: id + offset, level: level, text: text)
        case .code(let id, let language, let code): .code(id: id + offset, language: language, code: code)
        case .quote(let id, let blocks): .quote(id: id + offset, blocks: blocks)
        case .list(let id, let items): .list(id: id + offset, items: items)
        case .table(let id, let header, let rows): .table(id: id + offset, header: header, rows: rows)
        case .divider(let id): .divider(id: id + offset)
        }
    }
}

extension StringProtocol {
    fileprivate func trimmingSuffix(while predicate: (Character) -> Bool) -> SubSequence {
        var view = self[...]
        while let last = view.last, predicate(last) {
            view = view.dropLast()
        }
        return view
    }
}

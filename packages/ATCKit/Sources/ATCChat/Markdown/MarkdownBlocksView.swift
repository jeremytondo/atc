// The SwiftUI layout for `MarkdownBlock` lists: prose as `Text` with inline
// presentation intents, code on its own card with a language label and copy
// button, tables and quotes laid out natively. Syntax highlighting stays
// behind `CodeHighlighter` — the default does nothing; picking an engine is
// a later, isolated choice.

import ATCDesign
import SwiftUI

/// A pluggable code-highlighting seam. The default highlights nothing.
public protocol CodeHighlighter {
    func highlight(_ code: String, language: String?) -> AttributedString
}

public struct PlainCodeHighlighter: CodeHighlighter {
    public init() {}

    public func highlight(_ code: String, language: String?) -> AttributedString {
        AttributedString(code)
    }
}

extension EnvironmentValues {
    @Entry public var codeHighlighter: any CodeHighlighter = PlainCodeHighlighter()
}

/// A list of markdown blocks, stacked with prose spacing.
struct MarkdownBlocksView: View {
    let blocks: [MarkdownBlock]

    var body: some View {
        VStack(alignment: .leading, spacing: Spacing.md) {
            ForEach(blocks) { block in
                MarkdownBlockView(block: block)
            }
        }
        .font(Prose.font)
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

private struct MarkdownBlockView: View {
    let block: MarkdownBlock

    var body: some View {
        switch block {
        case .paragraph(_, let text):
            Text(text)
                .lineSpacing(Prose.lineSpacing)
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
        case .heading(_, let level, let text):
            Text(text)
                .font(Prose.heading(level))
                .foregroundStyle(.primary)
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.top, level <= 2 ? Spacing.xs : 0)
        case .code(_, let language, let code):
            CodeBlockView(language: language, code: code)
        case .quote(_, let blocks):
            MarkdownBlocksView(blocks: blocks)
                .foregroundStyle(.secondary)
                .padding(.leading, Spacing.md)
                .overlay(alignment: .leading) {
                    RoundedRectangle(cornerRadius: 1.5)
                        .fill(Surface.chipBorder)
                        .frame(width: 3)
                }
        case .list(_, let items):
            VStack(alignment: .leading, spacing: Spacing.sm) {
                ForEach(items) { item in
                    HStack(alignment: .firstTextBaseline, spacing: Spacing.sm) {
                        Text(item.marker)
                            .foregroundStyle(.secondary)
                            .monospacedDigit()
                        MarkdownBlocksView(blocks: item.blocks)
                    }
                }
            }
        case .table(_, let header, let rows):
            MarkdownTableView(header: header, rows: rows)
        case .divider:
            Rectangle().fill(Surface.chipBorder).frame(height: 1)
                .padding(.vertical, Spacing.xs)
        }
    }

}

/// A fenced code block: language label and copy button above monospaced,
/// horizontally scrolling code on a raised card.
struct CodeBlockView: View {
    let language: String?
    let code: String

    @Environment(\.codeHighlighter) private var highlighter

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: Spacing.sm) {
                Text(language ?? "code")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                Spacer(minLength: Spacing.md)
                CopyButton(text: code)
            }
            .padding(.horizontal, Spacing.sm)
            .padding(.vertical, Spacing.xs)
            Divider()
            ScrollView(.horizontal) {
                Text(highlighter.highlight(code, language: language))
                    .font(.callout.monospaced())
                    .textSelection(.enabled)
                    .padding(Spacing.sm)
            }
            .scrollIndicators(.automatic)
        }
        .background(Surface.card, in: RoundedRectangle(cornerRadius: Radius.control))
        .overlay(
            RoundedRectangle(cornerRadius: Radius.control)
                .strokeBorder(Surface.cardBorder)
        )
    }
}

/// A pipe table as a grid; wide tables scroll horizontally.
private struct MarkdownTableView: View {
    let header: [AttributedString]
    let rows: [[AttributedString]]

    var body: some View {
        ScrollView(.horizontal) {
            Grid(alignment: .leading, horizontalSpacing: Spacing.lg, verticalSpacing: Spacing.xs) {
                GridRow {
                    ForEach(Array(header.enumerated()), id: \.offset) { _, cell in
                        Text(cell).font(.callout.weight(.semibold))
                    }
                }
                Divider()
                ForEach(Array(rows.enumerated()), id: \.offset) { _, row in
                    GridRow {
                        ForEach(Array(row.enumerated()), id: \.offset) { _, cell in
                            Text(cell).font(.callout)
                        }
                    }
                }
            }
            .padding(Spacing.sm)
        }
        .scrollIndicators(.automatic)
        .background(Surface.card, in: RoundedRectangle(cornerRadius: Radius.control))
    }
}

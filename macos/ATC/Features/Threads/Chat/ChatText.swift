// Text rendering shared by the Chat rows: inline markdown for message text,
// and monospaced detail blocks (command output, diffs, JSON) for the
// expandable tool rows. Deliberately small — rendered code blocks and
// syntax-highlighted diffs are a later issue.

import ATCAppServerAPI
import OpenAPIRuntime
import SwiftUI

/// Message text with inline markdown (emphasis, code spans, links); block
/// structure is kept as plain paragraphs. Text that fails to parse renders
/// verbatim.
struct MarkdownText: View {
    let text: String

    var body: some View {
        Text(attributed)
            .textSelection(.enabled)
            .frame(maxWidth: .infinity, alignment: .leading)
    }

    private var attributed: AttributedString {
        (try? AttributedString(
            markdown: text,
            options: .init(interpretedSyntax: .inlineOnlyPreservingWhitespace)
        )) ?? AttributedString(text)
    }
}

/// A monospaced detail block: selectable, wrapping, on a raised surface.
struct DetailBlock: View {
    let text: String

    var body: some View {
        Text(text)
            .font(.callout.monospaced())
            .textSelection(.enabled)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(Spacing.sm)
            .background(Surface.card, in: RoundedRectangle(cornerRadius: Radius.control))
    }
}

/// A labelled detail block (e.g. "Arguments", "Output").
struct DetailSection<Content: View>: View {
    let title: String
    @ViewBuilder let content: () -> Content

    var body: some View {
        VStack(alignment: .leading, spacing: Spacing.xs) {
            Text(title)
                .font(.caption.weight(.semibold))
                .foregroundStyle(.secondary)
            content()
        }
    }
}

enum PrettyJSON {
    /// Any provider JSON (tool input/output, MCP arguments/result) as
    /// indented text; a value that cannot be re-encoded reads as `null`.
    static func string(_ value: OpenAPIValueContainer?) -> String {
        guard let value else { return "null" }
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.prettyPrinted, .sortedKeys, .withoutEscapingSlashes]
        guard let data = try? encoder.encode(value) else { return "null" }
        return String(decoding: data, as: UTF8.self)
    }
}

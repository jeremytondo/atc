// One view per transcript row kind. The visual model is the Codex desktop
// app: left-aligned assistant text with no bubbles, the user's prompt on a
// raised card, and compact tool rows — one line of adapter `title` + status —
// that disclose monospaced detail. Fidelity is deliberately "rows +
// expandable detail"; polish is a later issue.

import ATCAppServerAPI
import SwiftUI

struct ChatRowView: View {
    let row: ChatRow

    var body: some View {
        switch row {
        case .item(let node):
            ChatItemView(node: node)
        case .turnEnded(let turn):
            ChatTurnEndedRow(turn: turn)
        }
    }
}

struct ChatItemView: View {
    let node: ChatNode

    private var item: ThreadItem { node.item }
    private var children: [ChatNode] { node.children }

    var body: some View {
        // Tool rows keep their nested items inside the disclosure; anything
        // else shows them right below.
        VStack(alignment: .leading, spacing: Spacing.sm) {
            content
            if !children.isEmpty, !isToolRow {
                NestedItems(children: children)
            }
        }
    }

    private var isToolRow: Bool {
        switch item {
        case .command, .fileChange, .mcpCall, .toolCall: true
        case .userMessage, .assistantText, .reasoning, .compaction: false
        }
    }

    @ViewBuilder
    private var content: some View {
        switch item {
        case .userMessage(let message):
            MarkdownText(text: message.text)
                .padding(Spacing.md)
                .background(Surface.raised, in: RoundedRectangle(cornerRadius: Radius.chip))
        case .assistantText(let text):
            MarkdownText(text: text.text)
        case .reasoning(let reasoning):
            ChatThinkingRow(text: reasoning.text)
        case .command(let command):
            ChatToolRow(
                title: command.title, status: command.status, error: command.error, children: children
            ) {
                DetailSection(title: command.cwd.map { "$ \(command.command)  (\($0))" } ?? "$ \(command.command)") {
                    if let output = command.output, !output.isEmpty {
                        DetailBlock(text: output)
                    }
                    if let exitCode = command.exitCode {
                        Text("exit \(exitCode)")
                            .font(.caption.monospaced())
                            .foregroundStyle(exitCode == 0 ? Color.secondary : Color.red)
                    }
                }
            }
        case .fileChange(let change):
            ChatToolRow(title: change.title, status: change.status, error: change.error, children: children) {
                ForEach(Array(change.changes.enumerated()), id: \.offset) { _, file in
                    DetailSection(title: "\(file.kind.rawValue) \(file.path)") {
                        if let diff = file.diff, !diff.isEmpty {
                            DetailBlock(text: diff)
                        }
                    }
                }
            }
        case .mcpCall(let call):
            ChatToolRow(title: call.title, status: call.status, error: call.error, children: children) {
                DetailSection(title: "\(call.server) · \(call.tool) — arguments") {
                    DetailBlock(text: PrettyJSON.string(call.arguments))
                }
                if let result = call.result {
                    DetailSection(title: "Result") { DetailBlock(text: PrettyJSON.string(result)) }
                }
            }
        case .toolCall(let call):
            ChatToolRow(title: call.title, status: call.status, error: call.error, children: children) {
                DetailSection(title: "\(call.name) — input") {
                    DetailBlock(text: PrettyJSON.string(call.input))
                }
                if let output = call.output {
                    DetailSection(title: "Output") { DetailBlock(text: PrettyJSON.string(output)) }
                }
            }
        case .compaction:
            HStack(spacing: Spacing.sm) {
                Rectangle().fill(Surface.chipBorder).frame(height: 1)
                Text("Context compacted")
                    .font(.caption)
                    .foregroundStyle(.tertiary)
                    .fixedSize()
                Rectangle().fill(Surface.chipBorder).frame(height: 1)
            }
            .padding(.vertical, Spacing.xs)
        }
    }
}

/// Reasoning collapses to a "Thinking" line; the text streams while
/// collapsed and is there when the user opens it.
private struct ChatThinkingRow: View {
    let text: String
    @State private var isExpanded = false

    var body: some View {
        DisclosureGroup(isExpanded: $isExpanded) {
            MarkdownText(text: text)
                .foregroundStyle(.secondary)
                .padding(.top, Spacing.xs)
        } label: {
            Label("Thinking", systemImage: "brain")
                .font(.callout)
                .foregroundStyle(.secondary)
        }
    }
}

/// A tool item: adapter title + status, disclosing detail and any nested
/// items (subagent or nested-tool work) beneath it.
private struct ChatToolRow<Detail: View>: View {
    let title: String
    let status: ToolStatus
    let error: String?
    let children: [ChatNode]
    @ViewBuilder let detail: () -> Detail
    @State private var isExpanded = false

    var body: some View {
        DisclosureGroup(isExpanded: $isExpanded) {
            VStack(alignment: .leading, spacing: Spacing.sm) {
                detail()
                if let error {
                    Label(error, systemImage: "exclamationmark.triangle")
                        .font(.callout)
                        .foregroundStyle(.red)
                        .textSelection(.enabled)
                }
                if !children.isEmpty {
                    NestedItems(children: children)
                }
            }
            .padding(.top, Spacing.xs)
        } label: {
            HStack(spacing: Spacing.sm) {
                statusIndicator
                    .frame(width: 14)
                Text(title)
                    .font(.callout.monospaced())
                    .lineLimit(1)
                    .truncationMode(.middle)
                if !children.isEmpty {
                    Text("\(children.count)")
                        .font(.caption2.weight(.semibold))
                        .padding(.horizontal, Spacing.xs)
                        .padding(.vertical, 2)
                        .background(Surface.raised, in: Capsule())
                }
            }
            .foregroundStyle(status == .error ? Color.red : Color.primary)
        }
    }

    @ViewBuilder
    private var statusIndicator: some View {
        switch status {
        case .pending:
            Image(systemName: "clock").foregroundStyle(.secondary)
        case .running:
            ProgressView().controlSize(.mini)
        case .completed:
            Image(systemName: "checkmark").foregroundStyle(.secondary)
        case .error:
            Image(systemName: "xmark").foregroundStyle(.red)
        }
    }
}

/// Items that happened inside another (subagent / nested-tool work), set
/// in from the row that spawned them.
private struct NestedItems: View {
    let children: [ChatNode]

    var body: some View {
        VStack(alignment: .leading, spacing: Spacing.sm) {
            ForEach(children) { child in
                ChatItemView(node: child)
            }
        }
        .padding(.leading, Spacing.md)
        .overlay(alignment: .leading) {
            Rectangle().fill(Surface.chipBorder).frame(width: 1)
        }
    }
}

/// The one visible turn boundary: a turn that failed or was interrupted.
private struct ChatTurnEndedRow: View {
    let turn: ThreadTurn

    var body: some View {
        HStack(spacing: Spacing.sm) {
            Image(systemName: turn.status == .failed ? "exclamationmark.triangle" : "stop.circle")
            Text(turn.status == .failed ? (turn.error ?? "Turn failed") : "Interrupted")
                .textSelection(.enabled)
        }
        .font(.callout)
        .foregroundStyle(turn.status == .failed ? Color.red : Color.secondary)
    }
}

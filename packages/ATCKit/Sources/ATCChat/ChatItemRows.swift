// One view per transcript row kind. The visual model is the Codex desktop
// app: left-aligned assistant text with no bubbles, the user's prompt on a
// raised card, and compact tool rows — one line of adapter `title` + status —
// that disclose monospaced detail. Fidelity is deliberately "rows +
// expandable detail"; polish is a later issue.
//
// Every row observes its own `ChatItemModel` box: a streaming delta
// invalidates exactly one row, and disclosure expansion lives on the box so
// LazyVStack recycling cannot reset it (ATC-214).

import ATCAppServerAPI
import ATCDesign
import SwiftUI

public struct ChatRowView: View {
    let row: ChatRowModel

    public init(row: ChatRowModel) {
        self.row = row
    }

    public var body: some View {
        switch row {
        case .item(let node):
            ChatItemView(node: node)
        case .turnEnded(let turn):
            ChatTurnEndedRow(turn: turn)
        case .pending(let prompt):
            ChatPendingPromptRow(prompt: prompt)
        }
    }
}

struct ChatItemView: View {
    let node: ChatNodeModel

    private var item: ThreadItem { node.box.item }
    private var children: [ChatNodeModel] { node.children }

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
            ChatThinkingRow(box: node.box, text: reasoning.text)
        case .command(let command):
            ChatToolRow(
                box: node.box, title: command.title, status: command.status,
                error: command.error, children: children
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
            ChatToolRow(
                box: node.box, title: change.title, status: change.status,
                error: change.error, children: children
            ) {
                ForEach(Array(change.changes.enumerated()), id: \.offset) { _, file in
                    DetailSection(title: "\(file.kind.rawValue) \(file.path)") {
                        if let diff = file.diff, !diff.isEmpty {
                            DetailBlock(text: diff)
                        }
                    }
                }
            }
        case .mcpCall(let call):
            ChatToolRow(
                box: node.box, title: call.title, status: call.status,
                error: call.error, children: children
            ) {
                DetailSection(title: "\(call.server) · \(call.tool) — arguments") {
                    DetailBlock(text: node.box.pretty("arguments", call.arguments))
                }
                if let result = call.result {
                    DetailSection(title: "Result") { DetailBlock(text: node.box.pretty("result", result)) }
                }
            }
        case .toolCall(let call):
            ChatToolRow(
                box: node.box, title: call.title, status: call.status,
                error: call.error, children: children
            ) {
                DetailSection(title: "\(call.name) — input") {
                    DetailBlock(text: node.box.pretty("input", call.input))
                }
                if let output = call.output {
                    DetailSection(title: "Output") { DetailBlock(text: node.box.pretty("output", output)) }
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

/// The optimistic echo of a prompt this client just sent: styled like the
/// real user message it will become, so resolution is a swap, not a jump.
private struct ChatPendingPromptRow: View {
    let prompt: PendingPrompt

    var body: some View {
        MarkdownText(text: prompt.text)
            .padding(Spacing.md)
            .background(Surface.raised, in: RoundedRectangle(cornerRadius: Radius.chip))
    }
}

/// Reasoning collapses to a "Thinking" line; the text streams while
/// collapsed and is there when the user opens it.
private struct ChatThinkingRow: View {
    @Bindable var box: ChatItemModel
    let text: String

    var body: some View {
        DisclosureGroup(isExpanded: $box.isExpanded) {
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
    @Bindable var box: ChatItemModel
    let title: String
    let status: ToolStatus
    let error: String?
    let children: [ChatNodeModel]
    @ViewBuilder let detail: () -> Detail

    var body: some View {
        DisclosureGroup(isExpanded: $box.isExpanded) {
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
    let children: [ChatNodeModel]

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

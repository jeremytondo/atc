// One view per transcript row kind. The visual model is the Codex desktop
// app: left-aligned assistant prose with no bubbles — markdown rendered as
// real blocks (code as code, headings as headings) — the user's prompt on a
// raised card, and each turn's tool/reasoning work folded behind one
// summary row (`ChatWorkRow`). Copy is everywhere: hover button and context
// menu on messages, code, and tool detail. Timestamps appear only once a
// row's turn has settled, so nothing flashes while streaming.
//
// `ChatNodeView` is the one dispatcher over an item's kind — top-level rows
// and nested children (subagent / nested-tool work) both go through it, and
// it alone attaches the pending request blocked on the item. Every row
// observes its own box (`ChatItemModel` / `ChatWorkModel`): a streaming
// delta invalidates exactly one row, and disclosure state lives on the box
// so LazyVStack recycling cannot reset it (ATC-214).

import ATCAppServerAPI
import ATCDesign
import SwiftUI

/// The actions rows can trigger, injected by `ChatTranscriptView` so row
/// views stay data-driven.
struct ChatRowActions {
    var retry: (String) -> Void = { _ in }
    var answer: (String, ThreadRequestAnswer) -> Void = { _, _ in }
    /// The bytes of an attachment on a user row, decoded (nil = unreadable).
    var loadAttachment: (Components.Schemas.ThreadAttachment) async -> CGImage? = { _ in nil }
}

extension EnvironmentValues {
    @Entry var chatRowActions = ChatRowActions()
}

struct ChatRowView: View {
    let row: ChatRowModel

    var body: some View {
        switch row {
        case .item(let node):
            ChatNodeView(node: node)
        case .work(let work):
            ChatWorkRow(work: work)
        case .turnEnded(let ended):
            ChatTurnEndedRow(ended: ended)
        case .pending(let prompt):
            ChatPendingPromptRow(prompt: prompt)
        }
    }
}

/// Any rendered node — prose, thinking, or a tool step — plus the nested
/// items and pending request that belong to it.
struct ChatNodeView: View {
    let node: ChatNodeModel

    var body: some View {
        VStack(alignment: .leading, spacing: Spacing.sm) {
            content
            // Tool rows keep their children inside the disclosure; prose
            // shows them right below.
            if !node.children.isEmpty, !isToolRow {
                NestedItems(children: node.children)
            }
            if let request = node.request {
                InlineRequestCard(request: request)
            }
        }
    }

    private var isToolRow: Bool {
        switch node.box.item {
        case .command, .fileChange, .mcpCall, .toolCall: true
        case .userMessage, .assistantText, .reasoning, .compaction, .notice: false
        }
    }

    @ViewBuilder
    private var content: some View {
        switch node.box.item {
        case .userMessage(let message):
            VStack(alignment: .trailing, spacing: Spacing.xs) {
                if let attachments = message.attachments, !attachments.isEmpty {
                    AttachmentStrip(images: attachments.map { .remote($0) })
                }
                if !message.text.isEmpty {
                    MarkdownText(text: message.text)
                        .padding(Spacing.md)
                        .background(Surface.raised, in: RoundedRectangle(cornerRadius: Radius.chip))
                        .copyable(message.text)
                }
                if node.turnIsSettled, let createdAt = message.createdAt {
                    TimestampCaption(date: createdAt)
                }
            }
        case .assistantText(let text):
            MarkdownBlocksView(blocks: node.box.markdownBlocks(text: text.text, complete: text.complete))
                .foregroundStyle(TextColor.body)
                .copyable(text.text)
        case .compaction:
            ChatDividerRow(label: "Context compacted")
        case .notice(let notice):
            ChatNoticeRow(text: notice.text)
        case .reasoning(let reasoning):
            ChatThinkingRow(box: node.box, text: reasoning.text, complete: reasoning.complete)
        case .command(let command):
            ChatToolRow(node: node, title: command.title, status: command.status, error: command.error) {
                DetailSection(title: command.cwd.map { "$ \(command.command)  (\($0))" } ?? "$ \(command.command)") {
                    if let output = command.output, !output.isEmpty {
                        DetailBlock(text: output)
                            .copyable(output)
                    }
                    if let exitCode = command.exitCode {
                        Text("exit \(exitCode)")
                            .font(.caption.monospaced())
                            .foregroundStyle(exitCode == 0 ? Color.secondary : Color.red)
                    }
                }
            }
        case .fileChange(let change):
            ChatToolRow(node: node, title: change.title, status: change.status, error: change.error) {
                ForEach(Array(change.changes.enumerated()), id: \.offset) { _, file in
                    DetailSection(title: "\(file.kind.rawValue) \(file.path)") {
                        if let diff = file.diff, !diff.isEmpty {
                            DetailBlock(text: diff)
                                .copyable(diff)
                        }
                    }
                }
            }
        case .mcpCall(let call):
            ChatToolRow(node: node, title: call.title, status: call.status, error: call.error) {
                DetailSection(title: "\(call.server) · \(call.tool) — arguments") {
                    DetailBlock(text: node.box.pretty("arguments", call.arguments))
                        .copyable(node.box.pretty("arguments", call.arguments))
                }
                if let result = call.result {
                    DetailSection(title: "Result") {
                        DetailBlock(text: node.box.pretty("result", result))
                            .copyable(node.box.pretty("result", result))
                    }
                }
            }
        case .toolCall(let call):
            ChatToolRow(node: node, title: call.title, status: call.status, error: call.error) {
                DetailSection(title: "\(call.name) — input") {
                    DetailBlock(text: node.box.pretty("input", call.input))
                        .copyable(node.box.pretty("input", call.input))
                }
                if let output = call.output {
                    DetailSection(title: "Output") {
                        DetailBlock(text: node.box.pretty("output", output))
                            .copyable(node.box.pretty("output", output))
                    }
                }
            }
        }
    }
}

/// The one visible turn boundary: a turn that failed or was interrupted,
/// with Retry when the prompt it ran is on the page.
private struct ChatTurnEndedRow: View {
    let ended: ChatTurnEnded

    @Environment(\.chatRowActions) private var actions

    private var failed: Bool { ended.turn.status == .failed }

    var body: some View {
        HStack(spacing: Spacing.sm) {
            Image(systemName: failed ? "exclamationmark.triangle" : "stop.circle")
            Text(failed ? (ended.turn.error ?? "Turn failed") : "Interrupted")
                .textSelection(.enabled)
            if let prompt = ended.retryPrompt {
                Button("Retry") { actions.retry(prompt) }
                    .buttonStyle(.plain)
                    .foregroundStyle(Color.accentColor)
                    .help("Send the prompt again")
            }
        }
        .font(.callout)
        .foregroundStyle(failed ? Color.red : Color.secondary)
    }
}

/// The optimistic echo of a prompt this client just sent: styled like the
/// real user message it will become, so resolution is a swap, not a jump.
private struct ChatPendingPromptRow: View {
    let prompt: PendingPrompt

    var body: some View {
        VStack(alignment: .trailing, spacing: Spacing.xs) {
            if !prompt.attachments.isEmpty {
                AttachmentStrip(images: prompt.attachments.map { .local($0) })
            }
            if !prompt.text.isEmpty {
                MarkdownText(text: prompt.text)
                    .padding(Spacing.md)
                    .background(Surface.raised, in: RoundedRectangle(cornerRadius: Radius.chip))
            }
        }
    }
}

/// A labelled horizontal rule (compaction, and any future divider).
struct ChatDividerRow: View {
    let label: String

    var body: some View {
        HStack(spacing: Spacing.sm) {
            Rectangle().fill(Surface.chipBorder).frame(height: 1)
            Text(label)
                .font(.caption)
                .foregroundStyle(.secondary)
                .fixedSize()
            Rectangle().fill(Surface.chipBorder).frame(height: 1)
        }
        .padding(.vertical, Spacing.xs)
    }
}

/// A quiet centered system line: what ATC itself appended to the transcript
/// (a lost provider session), never provider content.
struct ChatNoticeRow: View {
    let text: String

    var body: some View {
        Text(text)
            .font(.caption)
            .foregroundStyle(.secondary)
            .multilineTextAlignment(.center)
            .frame(maxWidth: .infinity)
            .padding(.vertical, Spacing.xs)
    }
}

/// A short time under a settled row (withheld while running so nothing
/// flashes mid-stream).
struct TimestampCaption: View {
    let date: Date

    var body: some View {
        Text(date, style: .time)
            .font(.caption2)
            .foregroundStyle(.secondary)
    }
}

/// A pending request rendered in place, on the row of the item it blocks.
private struct InlineRequestCard: View {
    let request: ThreadRequest

    @Environment(\.chatRowActions) private var actions

    var body: some View {
        ChatRequestContentView(request: request) { answer in
            actions.answer(request.id, answer)
        }
        // The row's identity is the item's; keying the content on the
        // request resets its chosen/typed state when a new request lands
        // on the same row.
        .id(request.id)
        .padding(Spacing.lg)
        .background(Surface.card, in: RoundedRectangle(cornerRadius: Radius.card))
        .overlay(
            RoundedRectangle(cornerRadius: Radius.card)
                .strokeBorder(Color.orange.opacity(0.4))
        )
    }
}

/// Reasoning: a quiet "Thinking" line that streams while collapsed and
/// discloses the full text as rendered markdown.
private struct ChatThinkingRow: View {
    @Bindable var box: ChatItemModel
    let text: String
    let complete: Bool

    var body: some View {
        DisclosureGroup(isExpanded: $box.isExpanded) {
            MarkdownBlocksView(blocks: box.markdownBlocks(text: text, complete: complete))
                .foregroundStyle(.secondary)
                .copyable(text)
                .padding(.top, Spacing.xs)
        } label: {
            HStack(spacing: Spacing.sm) {
                Label("Thinking", systemImage: "brain")
                if !complete {
                    ProgressView().controlSize(.mini)
                }
            }
            .font(.callout)
            .foregroundStyle(.secondary)
        }
    }
}

/// A tool step's disclosure row: status + title, detail and nested items
/// inside the disclosure.
private struct ChatToolRow<Detail: View>: View {
    let node: ChatNodeModel
    let title: String
    let status: ToolStatus
    let error: String?
    @ViewBuilder let detail: () -> Detail

    var body: some View {
        DisclosureGroup(isExpanded: Bindable(node.box).isExpanded) {
            VStack(alignment: .leading, spacing: Spacing.sm) {
                detail()
                if let error {
                    Label(error, systemImage: "exclamationmark.triangle")
                        .font(.callout)
                        .foregroundStyle(.red)
                        .textSelection(.enabled)
                }
                if !node.children.isEmpty {
                    NestedItems(children: node.children)
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
                if !node.children.isEmpty {
                    Text("\(node.children.count)")
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
                ChatNodeView(node: child)
            }
        }
        .padding(.leading, Spacing.md)
        .overlay(alignment: .leading) {
            Rectangle().fill(Surface.chipBorder).frame(width: 1)
        }
    }
}

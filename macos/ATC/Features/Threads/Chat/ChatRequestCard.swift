// A pending provider request (question or approval) as a card above the
// composer. Answers go straight to the server; the card disappears when the
// stream reports the request closed — by this client or any other.

import ATCAppServerAPI
import SwiftUI

struct ChatRequestCard: View {
    let request: ThreadRequest
    let answer: (ThreadRequestAnswer) -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: Spacing.md) {
            switch request {
            case .question(let question):
                QuestionRequestContent(request: question, answer: answer)
            case .approval(let approval):
                ApprovalRequestContent(request: approval, answer: answer)
            }
        }
        .padding(Spacing.lg)
        .background(Surface.card, in: RoundedRectangle(cornerRadius: Radius.card))
        .overlay(RoundedRectangle(cornerRadius: Radius.card).strokeBorder(Surface.cardBorder))
    }
}

private struct QuestionRequestContent: View {
    let request: Components.Schemas.ThreadRequestQuestion
    let answer: (ThreadRequestAnswer) -> Void
    /// Chosen labels per question id (freeform text counts as one label).
    @State private var chosen: [String: [String]] = [:]
    @State private var freeform: [String: String] = [:]

    var body: some View {
        ForEach(request.questions, id: \.id) { question in
            VStack(alignment: .leading, spacing: Spacing.sm) {
                if !question.header.isEmpty {
                    Text(question.header).font(.headline)
                }
                MarkdownText(text: question.question)
                ForEach(Array(question.options.enumerated()), id: \.offset) { _, option in
                    optionButton(question, option)
                }
                if question.freeform {
                    freeformField(question)
                }
            }
        }
        HStack {
            Spacer()
            // No default action on purpose: Return belongs to the composer,
            // and several cards can be up at once.
            Button("Answer") {
                answer(.question(.init(kind: .question, answers: .init(additionalProperties: answers))))
            }
            .disabled(!isComplete)
        }
    }

    private func optionButton(_ question: Components.Schemas.Question, _ option: Components.Schemas.QuestionOption)
        -> some View
    {
        let selected = chosen[question.id, default: []].contains(option.label)
        return Button {
            select(option.label, for: question)
        } label: {
            HStack(alignment: .firstTextBaseline, spacing: Spacing.sm) {
                Image(systemName: selected ? "checkmark.circle.fill" : "circle")
                    .foregroundStyle(selected ? Color.accentColor : .secondary)
                VStack(alignment: .leading, spacing: 2) {
                    Text(option.label)
                    if !option.description.isEmpty {
                        Text(option.description).font(.caption).foregroundStyle(.secondary)
                    }
                }
                Spacer(minLength: 0)
            }
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
    }

    private func select(_ label: String, for question: Components.Schemas.Question) {
        freeform[question.id] = nil
        guard question.multiSelect else {
            chosen[question.id] = [label]
            return
        }
        var labels = chosen[question.id, default: []]
        if let index = labels.firstIndex(of: label) {
            labels.remove(at: index)
        } else {
            labels.append(label)
        }
        chosen[question.id] = labels
    }

    @ViewBuilder
    private func freeformField(_ question: Components.Schemas.Question) -> some View {
        let binding = Binding(
            get: { freeform[question.id] ?? "" },
            set: { text in
                freeform[question.id] = text
                // Typing an answer replaces any picked option.
                if !text.isEmpty { chosen[question.id] = nil }
            }
        )
        if question.secret {
            SecureField("Type an answer", text: binding)
        } else {
            TextField("Type an answer", text: binding, axis: .vertical)
                .lineLimit(1...4)
        }
    }

    /// Every question has at least one label or some freeform text.
    private var isComplete: Bool {
        request.questions.allSatisfy { !answers[$0.id, default: []].isEmpty }
    }

    private var answers: [String: [String]] {
        var answers: [String: [String]] = [:]
        for question in request.questions {
            let text = freeform[question.id]?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
            let labels = chosen[question.id] ?? []
            answers[question.id] = text.isEmpty ? labels : [text]
        }
        return answers
    }
}

private struct ApprovalRequestContent: View {
    let request: Components.Schemas.ThreadRequestApproval
    let answer: (ThreadRequestAnswer) -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: Spacing.xs) {
            Text(request.title).font(.headline)
            if let reason = request.reason, !reason.isEmpty {
                Text(reason).font(.callout).foregroundStyle(.secondary)
            }
        }
        subject
        HStack(spacing: Spacing.sm) {
            Button("Allow") { decide(.accept) }
            Button("Allow for Session") { decide(.acceptForSession) }
            Spacer()
            Button("Deny") { decide(.decline) }
            Button("Deny & Stop", role: .destructive) { decide(.cancel) }
        }
    }

    @ViewBuilder
    private var subject: some View {
        switch request.subject {
        case .command(let command):
            DetailBlock(text: command.cwd.map { "$ \(command.command)\n(\($0))" } ?? "$ \(command.command)")
        case .fileChange(let change):
            VStack(alignment: .leading, spacing: Spacing.xs) {
                ForEach(Array(change.changes.enumerated()), id: \.offset) { _, file in
                    Text("\(file.kind.rawValue) \(file.path)")
                        .font(.callout.monospaced())
                }
            }
        case .tool(let tool):
            DetailSection(title: tool.name) {
                DetailBlock(text: PrettyJSON.string(tool.input))
            }
        }
    }

    private func decide(_ decision: Components.Schemas.ApprovalDecision) {
        answer(.approval(.init(kind: .approval, decision: decision)))
    }
}

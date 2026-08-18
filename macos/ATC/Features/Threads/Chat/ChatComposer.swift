// The prompt composer pinned under the transcript, shaped like the Codex
// desktop composer: one rounded card, the text growing from a single line
// to a few, and a round send button in the corner. Return sends,
// Shift-Return inserts a newline. Send is never disabled while connected —
// the server admits every prompt (idle starts a turn, busy queues it) — and
// a refused prompt keeps its text with the server's message inline. Stop
// shows only while the server is driving a turn.

import SwiftUI

struct ChatComposer: View {
    @Binding var text: String
    let isSending: Bool
    let showsStop: Bool
    let error: String?
    /// Advances whenever the window wants the composer focused.
    let focusRequest: UInt
    let send: () -> Void
    let stop: () -> Void

    @FocusState private var isFocused: Bool
    @State private var editorHeight: CGFloat = 24

    var body: some View {
        VStack(alignment: .leading, spacing: Spacing.sm) {
            if let error {
                Label(error, systemImage: "exclamationmark.triangle")
                    .font(.callout)
                    .foregroundStyle(.orange)
                    .textSelection(.enabled)
            }
            VStack(alignment: .leading, spacing: Spacing.sm) {
                editor
                HStack(spacing: Spacing.sm) {
                    Spacer()
                    if showsStop {
                        Button(action: stop) {
                            Label("Stop", systemImage: "stop.fill")
                                .labelStyle(.titleAndIcon)
                                .font(.callout.weight(.medium))
                        }
                        .buttonStyle(.plain)
                        .padding(.horizontal, Spacing.md)
                        .padding(.vertical, Spacing.xs)
                        .background(Surface.raised, in: Capsule())
                        .help("Stop the running turn")
                    }
                    Button(action: send) {
                        Image(systemName: "arrow.up")
                            .font(.body.weight(.bold))
                            .frame(width: 30, height: 30)
                            .foregroundStyle(canSend ? Color.black : Color.secondary)
                            .background(canSend ? Color.white : Surface.raised, in: Circle())
                    }
                    .buttonStyle(.plain)
                    .disabled(!canSend)
                    .help("Send (Return)")
                    .accessibilityLabel("Send message")
                }
            }
            .padding(Spacing.md)
            .background(Surface.raised, in: RoundedRectangle(cornerRadius: Radius.card + Spacing.xs))
            .overlay(RoundedRectangle(cornerRadius: Radius.card + Spacing.xs).strokeBorder(Surface.chipBorder))
        }
        .onAppear { isFocused = true }
        .onChange(of: focusRequest) { _, _ in isFocused = true }
    }

    /// A TextEditor sized by the text behind it: it grows with the message
    /// from one line to a few, then scrolls. TextEditor claims every point
    /// it is offered, so its height is pinned to the measured text.
    private var editor: some View {
        ZStack(alignment: .topLeading) {
            Text(text.isEmpty ? " " : text)
                .font(.body)
                .lineLimit(1...8)
                .padding(.vertical, Spacing.xs)
                .padding(.horizontal, Spacing.xs + 1)
                .frame(maxWidth: .infinity, alignment: .leading)
                .hidden()
                .onGeometryChange(for: CGFloat.self) {
                    $0.size.height
                } action: {
                    editorHeight = $0
                }
            TextEditor(text: $text)
                .font(.body)
                .scrollContentBackground(.hidden)
                .scrollIndicators(.never)
                .focused($isFocused)
                .frame(height: editorHeight)
                .onKeyPress(.return, phases: .down) { press in
                    guard !press.modifiers.contains(.shift) else { return .ignored }
                    guard canSend else { return .handled }
                    send()
                    return .handled
                }
            if text.isEmpty {
                Text("Message the agent…")
                    .foregroundStyle(.tertiary)
                    .padding(.vertical, Spacing.xs)
                    .padding(.horizontal, Spacing.xs + 1)
                    .allowsHitTesting(false)
            }
        }
    }

    private var canSend: Bool {
        !isSending && !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }
}

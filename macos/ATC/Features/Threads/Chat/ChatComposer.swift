// The prompt composer floating over the transcript's tail, shaped like the
// Codex desktop composer: one rounded Liquid Glass card, the text starting
// a few lines tall and growing to more, the thread's settings controls
// leading the bottom row (ChatSettingsControls, taking every point the send
// button leaves so they fold only when the row truly is too narrow), and a
// round send button in the corner.
// Return sends (never mid-IME-composition — a marked-text Return commits the
// composition), Shift-Return inserts a newline, Escape stops a running turn,
// and ⌘↑ recalls the last sent prompt into an empty composer. Send is never
// disabled while connected — the server admits every prompt (idle starts a
// turn, busy queues it) — and a refused prompt keeps its text with the
// server's message inline. Stop shows only while the server is driving a
// turn. The composer autofocuses on appear unless a request card is up —
// answering the agent owns the keyboard until it is done.

import AppKit
import SwiftUI

struct ChatComposer: View {
    @Binding var text: String
    let thread: ATCThread
    /// The agent's model catalog, nil until read (the chip shows the raw id).
    let models: [AgentModel]?
    let modelsError: String?
    let showsStop: Bool
    let error: String?
    /// ⌘↑ recalls this into an empty composer.
    let lastSentPrompt: String?
    /// A request card is up: don't steal focus from it on appear.
    let yieldsFocus: Bool
    /// Advances whenever the window wants the composer focused.
    let focusRequest: UInt
    let send: () -> Void
    let stop: () -> Void
    let updateSettings: (ThreadSettingsPatch) -> Void
    let reloadModels: () -> Void

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
                    ChatSettingsControls(
                        thread: thread, models: models, modelsError: modelsError,
                        update: updateSettings, reloadModels: reloadModels
                    )
                    .frame(maxWidth: .infinity, alignment: .leading)
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
                        .help("Stop the running turn (Esc)")
                        .transition(.opacity)
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
            // The composer floats over the transcript, so it is glass like
            // the toolbar controls, not a card on the canvas.
            .glassEffect(in: RoundedRectangle(cornerRadius: Radius.card + Spacing.xs))
        }
        .onAppear {
            if !yieldsFocus { isFocused = true }
        }
        .onChange(of: focusRequest) { _, _ in isFocused = true }
    }

    /// A TextEditor sized by the text behind it: it starts three lines tall
    /// (room to type before the controls row), grows with the message, then
    /// scrolls. TextEditor claims every point it is offered, so its height
    /// is pinned to the measured text.
    private var editor: some View {
        ZStack(alignment: .topLeading) {
            Text(text.isEmpty ? " " : text)
                .font(.body)
                .lineLimit(3...10)
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
                    // Mid-IME composition, Return commits the marked text.
                    guard !isComposing else { return .ignored }
                    guard canSend else { return .handled }
                    send()
                    return .handled
                }
                .onKeyPress(.escape, phases: .down) { _ in
                    guard showsStop else { return .ignored }
                    stop()
                    return .handled
                }
                .onKeyPress(.upArrow, phases: .down) { press in
                    // Exactly ⌘↑ — ⌘⇧↑ and friends keep their standard text
                    // behavior (extend selection to start, …).
                    guard press.modifiers.subtracting(.capsLock) == .command else {
                        return .ignored
                    }
                    // Recall only into an empty composer; otherwise the text
                    // view keeps its standard ⌘↑ (caret to start).
                    guard text.isEmpty, let lastSentPrompt else { return .ignored }
                    text = lastSentPrompt
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
        !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    /// The focused editor holds uncommitted IME marked text.
    private var isComposing: Bool {
        guard let view = NSApp.keyWindow?.firstResponder as? NSTextView else { return false }
        return view.hasMarkedText()
    }
}

// The prompt composer floating over the transcript's tail, shaped like the
// Codex desktop composer: one rounded Liquid Glass card, the text starting
// a few lines tall and growing to more, the thread's settings controls
// leading the bottom row (ChatSettingsControls, taking every point the send
// button leaves so they fold only when the row truly is too narrow), and a
// round send button in the corner.
// Return sends (never mid-IME-composition — a marked-text Return commits the
// composition), Shift-Return inserts a newline, Escape stops a running turn,
// and ⌘↑/⌘↓ walk the thread's previous prompts (`PromptHistory`). Send is
// never disabled while connected — the server admits every prompt (idle
// starts a turn, busy queues it) — and a refused prompt keeps its text with
// the server's message inline. Stop shows only while the server is driving
// a turn, and then Return hands the prompt to that turn ("now") while
// ⌥Return queues it behind; idle, Return is a plain send. The composer
// autofocuses on appear unless a request is pending (a bar card or an
// inline row) — answering the agent owns the keyboard until it is done.
//
// Images (ATC-216) arrive by paste (the text view declines an image-only
// pasteboard, so the paste command reaches us), drop, or the Attach button
// (⌘⇧A); each is fitted to the server's caps at once
// (`ImageAttachmentEncoder`) and held as bytes until send. A prompt may be
// images alone.
//
// Completions (ATC-216): `@` at a word start lists the thread directory's
// files (the server ranks them; the query is debounced), `/` at the start
// lists the agent's commands (filtered here). The list sits inside the
// card above the text — a popover would take key focus from the editor —
// and is keyboard-first: ↑/↓ (Ctrl-N/P) move, Return/Tab accept, Esc
// dismisses; the accepted text replaces the trigger as plain text
// (`ComposerCompletion`).

import ATCAppServerAPI
import ATCChat
import ATCDesign
import AppKit
import SwiftUI
import UniformTypeIdentifiers

struct ChatComposer: View {
    @Binding var text: String
    @Binding var attachments: [PendingAttachment]
    let thread: ATCThread
    /// The agent's model catalog, nil until read (the chip shows the raw id).
    let models: [AgentModel]?
    let modelsError: String?
    /// The server is driving a turn: Stop shows, Return joins the turn.
    let isTurnRunning: Bool
    let error: String?
    /// The thread's previous prompts, newest first (⌘↑/⌘↓).
    let history: [String]
    /// The agent's commands in the thread's directory, nil until read.
    let commands: [AgentCommand]?
    /// A request card is up: don't steal focus from it on appear.
    let yieldsFocus: Bool
    /// Advances whenever the window wants the composer focused.
    let focusRequest: UInt
    /// The server's ranked files for an `@` query (nil: unavailable).
    let searchFiles: (String) async -> Components.Schemas.FsFilesResponse?
    /// Ask for the command list (idempotent; `commands` fills in).
    let loadCommands: () -> Void
    /// Send the draft; `when` is nil for a plain send while idle.
    let send: (PromptWhen?) -> Void
    let stop: () -> Void
    let updateSettings: (ThreadSettingsPatch) -> Void
    let reloadModels: () -> Void

    @FocusState private var isFocused: Bool
    @State private var selection: TextSelection?
    @State private var editorHeight: CGFloat = 24
    /// The last image that could not be attached (unreadable, too many).
    @State private var attachmentError: String?
    @State private var isPicking = false
    @State private var isDropTargeted = false
    @State private var completion: Completion?
    @State private var fileSearch: Task<Void, Never>?
    @State private var recall = PromptHistory()

    private static let imageTypes: [UTType] = [.image, .fileURL]
    private static let completionRowHeight: CGFloat = 26
    private static let completionVisibleRows = 8

    var body: some View {
        VStack(alignment: .leading, spacing: Spacing.sm) {
            if let error = error ?? attachmentError {
                Label(error, systemImage: "exclamationmark.triangle")
                    .font(.callout)
                    .foregroundStyle(.orange)
                    .textSelection(.enabled)
            }
            VStack(alignment: .leading, spacing: Spacing.sm) {
                if !attachments.isEmpty {
                    AttachmentStrip(images: attachments.map { .local($0) }) { image in
                        attachments.removeAll { $0.id.uuidString == image.id }
                    }
                }
                if let completion, !completion.items.isEmpty {
                    completionList(completion)
                }
                editor
                HStack(spacing: Spacing.sm) {
                    Button {
                        isPicking = true
                    } label: {
                        Image(systemName: "paperclip")
                            .font(.body.weight(.medium))
                            .frame(width: 24, height: 24)
                    }
                    .buttonStyle(.plain)
                    .foregroundStyle(.secondary)
                    .keyboardShortcut("a", modifiers: [.command, .shift])
                    .help("Attach image… (⌘⇧A)")
                    .accessibilityLabel("Attach image")
                    ChatSettingsControls(
                        thread: thread, models: models, modelsError: modelsError,
                        update: updateSettings, reloadModels: reloadModels
                    )
                    .frame(maxWidth: .infinity, alignment: .leading)
                    if isTurnRunning {
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
                        sendMenu
                    }
                    sendButton
                }
            }
            .padding(Spacing.md)
            // The composer floats over the transcript, so it is glass like
            // the toolbar controls, not a card on the canvas.
            .glassEffect(in: RoundedRectangle(cornerRadius: Radius.card + Spacing.xs))
            .overlay {
                if isDropTargeted {
                    RoundedRectangle(cornerRadius: Radius.card + Spacing.xs)
                        .strokeBorder(Color.accentColor, lineWidth: 2)
                }
            }
        }
        .onDrop(of: Self.imageTypes, isTargeted: $isDropTargeted) { providers in
            Task { attach(await ImagePasteboard.images(from: providers)) }
            return true
        }
        .fileImporter(
            isPresented: $isPicking, allowedContentTypes: [.image], allowsMultipleSelection: true,
            onCompletion: attachPicked
        )
        .onAppear {
            if !yieldsFocus { isFocused = true }
        }
        .onChange(of: focusRequest) { _, _ in isFocused = true }
        .onChange(of: text) { _, now in
            // Typing ends a history walk; a step of the walk does not.
            if let shown = recall.shown, shown != now { recall.reset() }
            refreshCompletion()
        }
        .onChange(of: selection) { _, _ in refreshCompletion() }
        .onChange(of: commands) { _, _ in
            if completion?.trigger.kind == .command { refreshCompletion() }
        }
    }

    // MARK: - Sending

    /// What Return sends: the running turn takes the prompt now, ⌥ queues it
    /// behind; idle, the plain send.
    private func whenFor(option: Bool) -> PromptWhen? {
        guard isTurnRunning else { return nil }
        return option ? .queue : .now
    }

    private var sendButton: some View {
        Button {
            send(whenFor(option: NSEvent.modifierFlags.contains(.option)))
        } label: {
            Image(systemName: "arrow.up")
                .font(.body.weight(.bold))
                .frame(width: 30, height: 30)
                .foregroundStyle(canSend ? Color.black : Color.secondary)
                .background(canSend ? Color.white : Surface.raised, in: Circle())
        }
        .buttonStyle(.plain)
        .disabled(!canSend)
        .help(isTurnRunning ? "Send now (Return) · Queue for after (⌥Return)" : "Send (Return)")
        .accessibilityLabel(isTurnRunning ? "Send now" : "Send message")
    }

    /// The running turn's alternative, spelled out: a quiet chevron popping
    /// the two ways a prompt can go.
    private var sendMenu: some View {
        PopupMenuButton(
            entries: [
                .item(title: "Send now", isSelected: false) { send(.now) },
                .item(title: "Queue for after", isSelected: false) { send(.queue) },
            ],
            appearance: .accessoryBar
        ) {
            Image(systemName: "chevron.down")
                .font(.caption.weight(.semibold))
                .frame(width: 18, height: 24)
        }
        .disabled(!canSend)
        .help("Send now, or queue for after the turn")
        .accessibilityLabel("Send options")
        .transition(.opacity)
    }

    // MARK: - Attachments

    private func attachPicked(_ result: Result<[URL], any Error>) {
        guard case .success(let urls) = result else { return }
        Task { attach(await Task.detached { urls.compactMap(ImagePasteboard.image(at:)) }.value) }
    }

    /// Fits each image to the caps — off the main actor, since a large
    /// image decodes and re-encodes for a while — and appends it, up to the
    /// per-prompt limit; the first failure is reported inline and the rest
    /// still land.
    private func attach(_ images: [ImagePasteboard.Image]) {
        guard !images.isEmpty else { return }
        attachmentError = nil
        Task {
            for image in images {
                guard attachments.count < ImageAttachmentEncoder.maxPerPrompt else {
                    attachmentError = "At most \(ImageAttachmentEncoder.maxPerPrompt) images per message."
                    return
                }
                do {
                    let prepared = try await Task.detached {
                        try ImageAttachmentEncoder.prepare(image.data, name: image.name)
                    }.value
                    attachments.append(prepared)
                } catch {
                    attachmentError = "\(image.name ?? "Image"): \(error.localizedDescription)"
                }
            }
        }
    }

    // MARK: - Completions

    /// One list the composer offers: the trigger it answers and its rows.
    private struct Completion {
        let trigger: ComposerCompletion.Trigger
        var items: [CompletionItem] = []
        var selectedID: String?
        var truncated = false
        /// The rows answer an earlier query and are shown only to avoid a
        /// flash; they cannot be accepted until the fresh ones land.
        var stale = false
    }

    /// A completion with rows on screen — the only state that takes keys
    /// from the editor (an empty or pending one is invisible and inert).
    private var hasVisibleCompletion: Bool {
        !(completion?.items.isEmpty ?? true)
    }

    private enum CompletionItem: Identifiable {
        case file(Components.Schemas.FsFileEntry)
        case command(AgentCommand)

        var id: String {
            switch self {
            case .file(let entry): "file:\(entry.path)"
            case .command(let command): "command:\(command.name)"
            }
        }

        /// What acceptance inserts in place of the trigger.
        var insertion: String {
            switch self {
            case .file(let entry): "@\(entry.path)"
            case .command(let command): "/\(command.name)"
            }
        }

        var title: String {
            switch self {
            case .file(let entry): entry.name
            case .command(let command): "/\(command.name)"
            }
        }

        var detail: String? {
            switch self {
            case .file(let entry): entry.path == entry.name ? nil : entry.path
            case .command(let command):
                command.argumentHint.map { "\($0)  \(command.description)" } ?? command.description
            }
        }

        var systemImage: String {
            switch self {
            case .file: "doc"
            case .command: "terminal"
            }
        }
    }

    /// The caret: the selection's end, or the text's end when the editor
    /// reports none.
    private var caret: String.Index {
        if case .selection(let range) = selection?.indices, range.upperBound <= text.endIndex {
            return range.upperBound
        }
        return text.endIndex
    }

    /// Re-derive the list from the text and caret: a command list filters
    /// in place, a file list asks the server after a short debounce so a
    /// burst of keystrokes is one request.
    private func refreshCompletion() {
        guard let trigger = ComposerCompletion.trigger(in: text, caret: caret) else {
            dismissCompletion()
            return
        }
        switch trigger.kind {
        case .command:
            fileSearch?.cancel()
            loadCommands()
            let items = ComposerCompletion.filter(commands ?? [], query: trigger.query).map(CompletionItem.command)
            setCompletion(trigger: trigger, items: items, truncated: false)
        case .file:
            if completion?.trigger != trigger {
                // Keep the last rows up (stale) while the next query is in
                // flight; a different kind of trigger starts empty.
                let previous = completion?.trigger.kind == .file ? completion : nil
                completion = Completion(
                    trigger: trigger, items: previous?.items ?? [], selectedID: previous?.selectedID,
                    truncated: previous?.truncated ?? false, stale: true)
            }
            fileSearch?.cancel()
            fileSearch = Task {
                try? await Task.sleep(for: .milliseconds(100))
                guard !Task.isCancelled else { return }
                let response = await searchFiles(trigger.query)
                guard !Task.isCancelled, completion?.trigger == trigger else { return }
                setCompletion(
                    trigger: trigger, items: (response?.entries ?? []).map(CompletionItem.file),
                    truncated: response?.truncated ?? false)
            }
        }
    }

    private func setCompletion(trigger: ComposerCompletion.Trigger, items: [CompletionItem], truncated: Bool) {
        let kept = completion?.selectedID.flatMap { id in items.first { $0.id == id }?.id }
        completion = Completion(
            trigger: trigger, items: items, selectedID: kept ?? items.first?.id, truncated: truncated)
    }

    private func dismissCompletion() {
        fileSearch?.cancel()
        fileSearch = nil
        completion = nil
    }

    private func moveCompletion(by offset: Int) -> KeyPress.Result {
        guard let current = completion, !current.items.isEmpty else { return .ignored }
        completion?.selectedID = SelectionMovement.wrapped(from: current.selectedID, by: offset, in: current.items)
        return .handled
    }

    private func acceptCompletion() -> KeyPress.Result {
        guard hasVisibleCompletion, let current = completion else { return .ignored }
        // Rows of an earlier query: swallow the key until the fresh ones land.
        guard !current.stale, let item = current.items.first(where: { $0.id == current.selectedID })
        else { return .handled }
        accept(item, for: current.trigger)
        return .handled
    }

    private func accept(_ item: CompletionItem, for trigger: ComposerCompletion.Trigger) {
        let accepted = ComposerCompletion.accept(trigger, in: text, with: item.insertion)
        dismissCompletion()
        text = accepted.text
        selection = TextSelection(range: accepted.caret..<accepted.caret)
    }

    private func completionList(_ completion: Completion) -> some View {
        VStack(alignment: .leading, spacing: Spacing.xs) {
            ScrollViewReader { proxy in
                ScrollView {
                    LazyVStack(alignment: .leading, spacing: 2) {
                        ForEach(completion.items) { item in
                            completionRow(item, isSelected: item.id == completion.selectedID)
                                .id(item.id)
                                .onTapGesture { accept(item, for: completion.trigger) }
                        }
                    }
                }
                .frame(
                    height: Self.completionRowHeight
                        * CGFloat(min(completion.items.count, Self.completionVisibleRows))
                )
                .onChange(of: completion.selectedID) { _, id in
                    if let id { proxy.scrollTo(id) }
                }
            }
            if completion.truncated {
                Text("More files than the index holds — type more of the name.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .padding(.horizontal, Spacing.sm)
            }
        }
        .padding(Spacing.xs)
        .background(Surface.raised, in: RoundedRectangle(cornerRadius: Radius.chip))
        .accessibilityLabel(completion.trigger.kind == .file ? "File suggestions" : "Command suggestions")
    }

    private func completionRow(_ item: CompletionItem, isSelected: Bool) -> some View {
        HStack(spacing: Spacing.sm) {
            Image(systemName: item.systemImage)
                .foregroundStyle(.secondary)
                .frame(width: 16)
            Text(item.title)
                .lineLimit(1)
            if let detail = item.detail {
                Text(detail)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .truncationMode(.middle)
            }
            Spacer(minLength: 0)
        }
        .font(.callout)
        .padding(.horizontal, Spacing.sm)
        .frame(height: Self.completionRowHeight)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(
            isSelected ? Color.accentColor.opacity(0.18) : Color.clear,
            in: RoundedRectangle(cornerRadius: Radius.chip - 2)
        )
        .contentShape(Rectangle())
    }

    // MARK: - Editor

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
            TextEditor(text: $text, selection: $selection)
                .font(.body)
                .scrollContentBackground(.hidden)
                .scrollIndicators(.never)
                .focused($isFocused)
                .frame(height: editorHeight)
                // The text view handles any paste it can read (text); an
                // image-only pasteboard falls through to this.
                .onPasteCommand(of: Self.imageTypes) { _ in
                    attach(ImagePasteboard.images(from: .general))
                }
                .onKeyPress(.return, phases: .down) { press in
                    guard !press.modifiers.contains(.shift) else { return .ignored }
                    // Mid-IME composition, every key is the input method's.
                    guard !isComposing else { return .ignored }
                    if hasVisibleCompletion { return acceptCompletion() }
                    guard canSend else { return .handled }
                    send(whenFor(option: press.modifiers.contains(.option)))
                    return .handled
                }
                .onKeyPress(.tab, phases: .down) { _ in
                    guard !isComposing else { return .ignored }
                    return acceptCompletion()
                }
                .onKeyPress(.escape, phases: .down) { _ in
                    guard !isComposing else { return .ignored }
                    if hasVisibleCompletion {
                        dismissCompletion()
                        return .handled
                    }
                    guard isTurnRunning else { return .ignored }
                    stop()
                    return .handled
                }
                .onKeyPress(.upArrow, phases: .down) { press in
                    verticalKey(press, offset: -1)
                }
                .onKeyPress(.downArrow, phases: .down) { press in
                    verticalKey(press, offset: 1)
                }
                .onKeyPress(keys: ["n", "p"], phases: .down) { press in
                    guard press.modifiers == .control else { return .ignored }
                    return moveCompletion(by: press.key == "n" ? 1 : -1)
                }
            if text.isEmpty {
                Text(attachments.isEmpty ? "Message the agent…" : "Add a message, or send the images…")
                    .foregroundStyle(.secondary)
                    .padding(.vertical, Spacing.xs)
                    .padding(.horizontal, Spacing.xs + 1)
                    .allowsHitTesting(false)
            }
        }
    }

    /// ↑/↓: move the completion while one is up; exactly ⌘↑/⌘↓ walk the
    /// prompt history (⌘⇧↑ and friends keep their standard text behavior);
    /// anything else is the text view's.
    private func verticalKey(_ press: KeyPress, offset: Int) -> KeyPress.Result {
        if hasVisibleCompletion, press.modifiers.isEmpty { return moveCompletion(by: offset) }
        guard press.modifiers.subtracting(.capsLock) == .command else { return .ignored }
        guard let recalled = offset < 0 ? recall.older(from: text, in: history) : recall.newer(in: history)
        else { return .ignored }
        text = recalled
        selection = TextSelection(range: recalled.endIndex..<recalled.endIndex)
        return .handled
    }

    private var canSend: Bool {
        !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || !attachments.isEmpty
    }

    /// The focused editor holds uncommitted IME marked text.
    private var isComposing: Bool {
        guard let view = NSApp.keyWindow?.firstResponder as? NSTextView else { return false }
        return view.hasMarkedText()
    }
}

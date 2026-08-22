// The Chat mode of a thread: the shared transcript (`ChatTranscriptView`,
// from ATCChat) with pending requests, the prompt queue, and the composer
// floating over its tail as a Liquid Glass bar — the transcript scrolls
// under the window toolbar above and that bar below, fading into both. The
// view holds the thread's Chat model for exactly as long as it is on screen
// (`acquireChat` / `releaseChat` on the Connection's runtime), so navigating
// away drops the subscription while a second window on the same thread
// keeps it. The composer draft lives on `AppModel`, so it
// survives leaving Chat.
//
// Requests split by where they render: one blocked on a transcript item
// shows inline on that row, and the bar keeps only a compact
// "pending · jump" chip for it; requests without an item stay cards in the
// bar. Motion (ATC-214): the model applies structural changes inside
// withAnimation, rows carry insert transitions, and the bottom bar's glass
// pieces share one container and namespace so appearing cards morph out of
// the composer instead of popping. Chat actions run through the model's
// `perform` — gated on this thread's stream, reported inline — never a
// modal.

import ATCAppServerAPI
import ATCChat
import ATCDesign
import SwiftUI

struct ThreadChatView: View {
    @Environment(AppModel.self) private var appModel
    @Environment(WindowState.self) private var windowState
    let ref: ThreadRef

    /// The hold this view currently has on a Chat model. Rendering is gated
    /// on it matching `ref`, and cleanup on its token, because holds overlap
    /// during a switch (the replacement task starts before the cancelled one
    /// unwinds) and `acquireChat` hands the same model to every holder.
    @State private var acquisition: Acquisition?

    var body: some View {
        Group {
            if let acquisition, acquisition.ref == ref, let thread = appModel.thread(for: ref) {
                ChatPane(
                    ref: ref, chat: acquisition.model, thread: thread, focusRequest: windowState.contentFocusRequest)
            } else {
                Color.clear
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(AppColors.canvas)
        // Keyed on the runtime object too: a Connection rebuild (URL/token
        // edit) replaces the runtime under the same ref, and the Chat must
        // move to the new one.
        .task(id: Hold(ref: ref, runtime: appModel.runtime(id: ref.connectionID).map(ObjectIdentifier.init))) {
            guard let runtime = appModel.runtime(id: ref.connectionID) else { return }
            let mine = Acquisition(ref: ref, model: runtime.acquireChat(threadID: ref.threadID))
            acquisition = mine
            defer {
                runtime.releaseChat(threadID: ref.threadID)
                if acquisition?.token == mine.token { acquisition = nil }
            }
            // The task's only job past this point is to be cancelled — when
            // the view leaves the screen or moves to another thread — so the
            // release above runs at exactly that moment.
            while !Task.isCancelled {
                try? await Task.sleep(for: .seconds(3600))
            }
        }
    }
}

/// What one Chat acquisition is bound to.
private struct Hold: Equatable {
    let ref: ThreadRef
    let runtime: ObjectIdentifier?
}

private struct Acquisition {
    let token = UUID()
    let ref: ThreadRef
    let model: ThreadChatModel
}

/// The Chat surface for one acquired model: transcript, requests, queue,
/// composer.
private struct ChatPane: View {
    @Environment(AppModel.self) private var appModel
    let ref: ThreadRef
    let chat: ThreadChatModel
    let thread: ATCThread
    let focusRequest: UInt

    /// Bumped to jump to the tail and follow it again (a sent prompt).
    @State private var followRequest = 0
    /// The bar chip's one-shot scroll to an inline request's row.
    @State private var jumpRequest: ChatJumpRequest?
    /// Measured height of the request/queue stack, capped by `cardsCap`.
    @State private var cardsHeight: CGFloat = 0
    @Namespace private var glassNamespace

    /// The request/queue stack scrolls inside itself past this height.
    private let cardsCap: CGFloat = 320

    private var agents: AgentsStore? { appModel.runtime(id: ref.connectionID)?.agents }

    private var draftBinding: Binding<ComposerDraft> {
        Binding(
            get: { appModel.draft(for: ref) },
            set: { appModel.setDraft($0, for: ref) }
        )
    }

    var body: some View {
        ChatTranscriptView(
            chat: chat,
            emptyTitle: thread.displayName,
            followRequest: followRequest,
            jumpRequest: jumpRequest
        )
        // The transcript scrolls under the toolbar above and the composer
        // bar below, fading into both with the system's soft edge effect.
        .scrollEdgeEffectUnderToolbar()
        .safeAreaBar(edge: .bottom) { bottomStack }
        .overlay(alignment: .top) { statusBanner }
        // The composer's model chip and menu need the agent's catalog.
        .task(id: thread.agentId) { agents?.loadModels(for: thread.agentId) }
    }

    // MARK: - Bottom stack

    /// Everything floating over the transcript's tail is Liquid Glass: one
    /// container renders the pieces together, and a shared namespace lets an
    /// appearing card or strip morph out of the composer's glass instead of
    /// popping in.
    private var bottomStack: some View {
        GlassEffectContainer {
            VStack(spacing: Spacing.sm) {
                if let actionError = chat.actionError {
                    actionErrorBanner(actionError)
                }
                if !chat.inlineRequests.isEmpty {
                    inlineRequestsChip
                }
                if !chat.barRequests.isEmpty || !chat.queue.isEmpty {
                    cards
                }
                ChatComposer(
                    text: draftBinding.text,
                    attachments: draftBinding.attachments,
                    thread: thread,
                    models: agents?.models(for: thread.agentId),
                    modelsError: agents?.modelErrors[thread.agentId],
                    isTurnRunning: chat.runningTurn != nil,
                    error: chat.promptError,
                    history: chat.promptHistory,
                    commands: agents?.commands(for: thread.agentId, dir: thread.workingDirectory),
                    yieldsFocus: !chat.requests.isEmpty,
                    focusRequest: focusRequest,
                    searchFiles: searchFiles,
                    loadCommands: { agents?.loadCommands(for: thread.agentId, dir: thread.workingDirectory) },
                    send: send,
                    stop: { chat.perform { [chat] in try await chat.interrupt() } },
                    updateSettings: { patch in
                        chat.perform { [appModel] in
                            try await appModel.runtime(id: ref.connectionID)?.threads
                                .updateSettings(id: ref.threadID, patch)
                        }
                    },
                    reloadModels: { agents?.loadModels(for: thread.agentId) }
                )
                .glassEffectID("composer", in: glassNamespace)
            }
        }
        .padding(.horizontal, Spacing.xxl)
        .padding(.top, Spacing.md)
        .padding(.bottom, Spacing.lg)
        .frame(maxWidth: 820)
        .frame(maxWidth: .infinity)
    }

    /// "1 approval pending" when every inline request is an approval;
    /// questions blocked on an item read as requests.
    private var chipLabel: String {
        let count = chat.inlineRequests.count
        let allApprovals = chat.inlineRequests.allSatisfy { request in
            if case .approval = request { return true }
            return false
        }
        let noun = allApprovals ? "approval" : "request"
        return "\(count) \(noun)\(count == 1 ? "" : "s") pending"
    }

    /// Requests answered in place keep the bar quiet: one compact chip that
    /// scrolls the transcript to the first blocked row.
    private var inlineRequestsChip: some View {
        HStack {
            Spacer()
            Button {
                guard let itemID = chat.inlineRequests.first?.itemId else { return }
                jumpRequest = ChatJumpRequest(itemID: itemID)
            } label: {
                HStack(spacing: Spacing.xs) {
                    Image(systemName: "questionmark.bubble")
                    Text(chipLabel)
                    Text("·").foregroundStyle(.tertiary)
                    Text("Jump")
                }
                .font(.callout)
                .padding(.horizontal, Spacing.md)
                .padding(.vertical, Spacing.xs)
            }
            .buttonStyle(.plain)
            .foregroundStyle(.orange)
            .glassEffect(in: Capsule())
            .glassEffectID("inlineRequests", in: glassNamespace)
            .help("Scroll to the pending approval")
            Spacer()
        }
        .transition(.opacity)
    }

    /// The request cards and queue strip, height-capped: past the cap the
    /// stack scrolls inside itself instead of swallowing the transcript.
    private var cards: some View {
        ScrollView {
            VStack(spacing: Spacing.sm) {
                ForEach(chat.barRequests, id: \.id) { request in
                    ChatRequestCard(request: request) { answer in
                        chat.perform { [chat] in try await chat.answer(requestID: request.id, answer) }
                    }
                    .glassEffectID("request-\(request.id)", in: glassNamespace)
                }
                if !chat.queue.isEmpty {
                    ChatQueueStrip(prompts: chat.queue) { prompt in
                        chat.perform { [chat] in try await chat.withdraw(promptID: prompt.id) }
                    }
                    .glassEffectID("queue", in: glassNamespace)
                }
            }
            .onGeometryChange(for: CGFloat.self) {
                $0.size.height
            } action: { height in
                // The bar's height rides this measurement: animate it so a
                // card appearing grows the bar instead of popping it.
                withAnimation(.snappy(duration: 0.25)) { cardsHeight = height }
            }
        }
        .scrollIndicators(.never)
        .frame(height: min(cardsHeight, cardsCap))
    }

    private func actionErrorBanner(_ message: String) -> some View {
        HStack(spacing: Spacing.sm) {
            Image(systemName: "exclamationmark.triangle").foregroundStyle(.orange)
            Text(message).lineLimit(2).textSelection(.enabled)
            Spacer(minLength: 0)
            Button {
                chat.clearActionError()
            } label: {
                Image(systemName: "xmark")
            }
            .buttonStyle(.plain)
            .foregroundStyle(.secondary)
            .accessibilityLabel("Dismiss error")
        }
        .font(.callout)
        .padding(.horizontal, Spacing.md)
        .padding(.vertical, Spacing.sm)
        .glassEffect(in: RoundedRectangle(cornerRadius: Radius.card + Spacing.xs))
        .glassEffectID("actionError", in: glassNamespace)
        .transition(.opacity)
    }

    /// The draft (text and images) clears the moment the prompt leaves (the
    /// echo row carries it); a refusal restores it in front of whatever was
    /// typed or attached since, so nothing is ever lost. `when` is the
    /// composer's choice while a turn runs (now / queue), nil when idle.
    private func send(when: PromptWhen?) {
        let draft = appModel.draft(for: ref)
        let prompt = draft.text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !prompt.isEmpty || !draft.attachments.isEmpty else { return }
        appModel.setDraft(ComposerDraft(), for: ref)
        followRequest += 1
        Task {
            guard await !chat.send(prompt, attachments: draft.attachments, when: when) else { return }
            let since = appModel.draft(for: ref)
            appModel.setDraft(
                ComposerDraft(
                    text: since.text.isEmpty ? prompt : prompt + "\n" + since.text,
                    attachments: draft.attachments + since.attachments),
                for: ref)
        }
    }

    /// The `@` completion's source: the server's ranking of the thread
    /// directory's files, a dozen at a time; nil when the read fails (the
    /// list simply stays empty).
    private func searchFiles(_ query: String) async -> Components.Schemas.FsFilesResponse? {
        guard let client = appModel.runtime(id: ref.connectionID)?.client else { return nil }
        return try? await client.searchFiles(
            query: .init(dir: thread.workingDirectory, query: query, limit: "12")
        ).ok.body.json
    }

    // MARK: - Status

    @ViewBuilder
    private var statusBanner: some View {
        switch chat.connection {
        case .connecting:
            FloatingBanner {
                ProgressView().controlSize(.small)
                Text("Connecting…")
            }
        case .reconnecting:
            FloatingBanner {
                ProgressView().controlSize(.small)
                Text("Reconnecting…")
            }
        case .live:
            EmptyView()
        }
    }
}

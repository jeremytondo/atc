// The Chat mode of a thread: the transcript, with pending requests, the
// prompt queue, and the composer floating over its tail as a Liquid Glass
// bar — the transcript scrolls under the window toolbar above and that bar
// below, fading into both. The view holds the thread's Chat
// model for exactly as long as it is on screen (`acquireChat` / `releaseChat`
// on the Connection's runtime), so navigating away or flipping to TUI drops
// the subscription while a second window on the same thread keeps it.
// The composer draft lives on `AppModel`, so it survives leaving Chat.
//
// The transcript follows the tail until the user scrolls up, and offers
// "Load earlier" at the top while older pages exist. Connection loss shows a
// floating "Reconnecting…" over a transcript that stays put — the stream
// resumes from its seq, so nothing is refetched.
//
// Motion (ATC-214): the model applies structural changes inside
// withAnimation, rows carry insert transitions, and the bottom bar's glass
// pieces share one container and namespace so appearing cards morph out of
// the composer instead of popping. Chat actions run through the model's
// `perform` — gated on this thread's stream, reported inline — never a modal.

import ATCAppServerAPI
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

    @State private var isFollowingTail = true
    /// Bumped to jump to the tail and follow it again (a sent prompt).
    @State private var followRequest = 0
    /// Measured height of the request/queue stack, capped by `cardsCap`.
    @State private var cardsHeight: CGFloat = 0
    @Namespace private var glassNamespace

    /// The request/queue stack scrolls inside itself past this height.
    private let cardsCap: CGFloat = 320

    private var agents: AgentsStore? { appModel.runtime(id: ref.connectionID)?.agents }

    private var draftBinding: Binding<String> {
        Binding(
            get: { appModel.draft(for: ref) },
            set: { appModel.setDraft($0, for: ref) }
        )
    }

    var body: some View {
        transcriptList
            .overlay(alignment: .top) { statusBanner }
            // The composer's model chip and menu need the agent's catalog.
            .task(id: thread.agentId) { agents?.loadModels(for: thread.agentId) }
    }

    // MARK: - Transcript

    private var transcriptList: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(alignment: .leading, spacing: Spacing.md) {
                    if chat.hasMore {
                        HStack {
                            Spacer()
                            Button("Load earlier") { loadOlder(proxy) }
                                .disabled(chat.isLoadingOlder)
                            Spacer()
                        }
                    }
                    if let error = chat.loadError {
                        loadFailure(error)
                    } else if !chat.isLoaded {
                        loadingIndicator
                    } else if chat.rows.isEmpty, !isWorking {
                        emptyState
                    }
                    ForEach(chat.rows) { row in
                        ChatRowView(row: row)
                            .id(row.id)
                            .transition(
                                .asymmetric(
                                    insertion: .opacity.combined(with: .offset(y: 12)),
                                    removal: .opacity
                                ))
                    }
                    if isWorking {
                        HStack(spacing: Spacing.sm) {
                            ProgressView().controlSize(.small)
                            Text("Working…").foregroundStyle(.secondary)
                        }
                        .font(.callout)
                        .transition(.opacity)
                    }
                    Color.clear.frame(height: 1).id(tailAnchor)
                }
                .padding(.horizontal, Spacing.xxl)
                .padding(.vertical, Spacing.lg)
                .frame(maxWidth: 820)
                .frame(maxWidth: .infinity)
            }
            // The transcript scrolls under the toolbar above and the
            // composer bar below, fading into both with the system's soft
            // edge effect.
            .scrollEdgeEffectUnderToolbar()
            .safeAreaBar(edge: .bottom) { bottomStack }
            // Tail-follow, T3Code style: while following, every content
            // or viewport size change re-pins the tail above the composer
            // (done from the geometry change, once layout has the new
            // sizes — a scroll issued when the model changes lands short of
            // rows that are not measured yet). Only the user's own scroll
            // gesture leaves follow mode: content growth moves the geometry
            // too, so it must never be what decides. Ending a gesture at the
            // tail resumes following.
            .onScrollGeometryChange(for: ChatTailLayout.self, of: ChatTailLayout.init) { _, _ in
                if isFollowingTail { proxy.scrollTo(tailAnchor, anchor: .bottom) }
            }
            .onScrollPhaseChange { previous, phase, context in
                // A gesture leaves follow mode; a gesture ending at the tail
                // resumes it. An idle that no gesture led to (the first
                // callback, a programmatic scroll settling) decides nothing.
                if phase.isGesture {
                    isFollowingTail = false
                } else if phase == .idle, previous.isGesture {
                    isFollowingTail = context.geometry.isAtTail
                }
            }
            .onChange(of: followRequest) { _, _ in
                isFollowingTail = true
                proxy.scrollTo(tailAnchor, anchor: .bottom)
            }
        }
    }

    /// Older history goes in above; the row that was first stays where the
    /// eye is instead of the tail-follow yanking the view back down.
    private func loadOlder(_ proxy: ScrollViewProxy) {
        guard let anchor = chat.rows.first?.id else { return }
        isFollowingTail = false
        chat.perform { [chat] in
            try await chat.loadOlder()
            proxy.scrollTo(anchor, anchor: .top)
        }
    }

    private let tailAnchor = "tail"

    /// The server drives a turn (Stop is offered), or the agent is busy under
    /// a TUI (items land at idle — the server's re-read).
    private var isWorking: Bool {
        chat.runningTurn != nil || thread.activityState == .working
    }

    private func loadFailure(_ error: String) -> some View {
        HStack(spacing: Spacing.sm) {
            Image(systemName: "exclamationmark.triangle").foregroundStyle(.orange)
            Text(error).lineLimit(2)
            Button("Retry") { Task { await chat.loadNewest() } }
        }
        .font(.callout)
    }

    private var loadingIndicator: some View {
        HStack(spacing: Spacing.sm) {
            ProgressView().controlSize(.small)
            Text("Loading transcript…").foregroundStyle(.secondary)
        }
        .font(.callout)
    }

    private var emptyState: some View {
        VStack(alignment: .leading, spacing: Spacing.xs) {
            Text(thread.displayName).font(.title3.weight(.semibold))
            Text("Send a message to start the conversation.")
                .foregroundStyle(.secondary)
        }
        .padding(.top, Spacing.xxl)
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
                if !chat.requests.isEmpty || !chat.queue.isEmpty {
                    cards
                }
                ChatComposer(
                    text: draftBinding,
                    thread: thread,
                    models: agents?.models(for: thread.agentId),
                    modelsError: agents?.modelErrors[thread.agentId],
                    showsStop: chat.runningTurn != nil,
                    error: chat.promptError,
                    lastSentPrompt: chat.lastSentPrompt,
                    yieldsFocus: !chat.requests.isEmpty,
                    focusRequest: focusRequest,
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

    /// The request cards and queue strip, height-capped: past the cap the
    /// stack scrolls inside itself instead of swallowing the transcript.
    private var cards: some View {
        ScrollView {
            VStack(spacing: Spacing.sm) {
                ForEach(chat.requests, id: \.id) { request in
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
            } action: {
                cardsHeight = $0
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

    /// The draft clears the moment the prompt leaves (the echo row carries
    /// it); a refusal restores it in front of whatever was typed since, so
    /// no text is ever lost.
    private func send() {
        let prompt = appModel.draft(for: ref).trimmingCharacters(in: .whitespacesAndNewlines)
        guard !prompt.isEmpty else { return }
        appModel.setDraft("", for: ref)
        followRequest += 1
        Task {
            guard await !chat.send(prompt) else { return }
            let typedSince = appModel.draft(for: ref)
            appModel.setDraft(
                typedSince.isEmpty ? prompt : prompt + "\n" + typedSince, for: ref)
        }
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

/// The sizes whose change moves the tail: the content's, the viewport's (a
/// window resize re-pins too), and the bars' — the composer growing with a
/// draft, a request card or the queue appearing — so a change to any of them
/// re-pins while following. Insets are tracked in their own right rather than
/// through the container they shrink.
struct ChatTailLayout: Equatable {
    let content: CGFloat
    let container: CGFloat
    let insetTop: CGFloat
    let insetBottom: CGFloat

    init(_ geometry: ScrollGeometry) {
        content = geometry.contentSize.height
        container = geometry.containerSize.height
        insetTop = geometry.contentInsets.top
        insetBottom = geometry.contentInsets.bottom
    }
}

extension ScrollPhase {
    /// The user is scrolling: their finger or wheel is driving it, or its
    /// momentum still is.
    var isGesture: Bool {
        switch self {
        case .tracking, .interacting, .decelerating: true
        case .idle, .animating: false
        @unknown default: false
        }
    }
}

extension ScrollGeometry {
    /// The toolbar and the composer bar are content insets: the container is
    /// the area between them, so the tail is in view when the container's
    /// bottom edge — offset plus insets plus container — reaches the
    /// content's end plus the bottom inset (with some slack).
    var isAtTail: Bool {
        let viewportBottom = contentOffset.y + contentInsets.top + containerSize.height + contentInsets.bottom
        return viewportBottom >= contentSize.height + contentInsets.bottom - Spacing.xxl
    }
}

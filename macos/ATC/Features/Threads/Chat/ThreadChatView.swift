// The Chat mode of a thread: the transcript, with pending requests, the
// prompt queue, and the composer floating over its tail as a Liquid Glass
// bar — the transcript scrolls under the window toolbar above and that bar
// below, fading into both. The view holds the thread's Chat
// model for exactly as long as it is on screen (`acquireChat` / `releaseChat`
// on the Connection's runtime), so navigating away or flipping to TUI drops
// the subscription while a second window on the same thread keeps it.
//
// The transcript follows the tail until the user scrolls up, and offers
// "Load earlier" at the top while older pages exist. Connection loss shows a
// floating "Reconnecting…" over a transcript that stays put — the stream
// resumes from its seq, so nothing is refetched.

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
/// composer. Actions run through the app's one mutation seam and report
/// into the standard action alert.
private struct ChatPane: View {
    @Environment(AppModel.self) private var appModel
    let ref: ThreadRef
    let chat: ThreadChatModel
    let thread: ATCThread
    let focusRequest: UInt

    @State private var draft = ""
    @State private var isSending = false
    @State private var isFollowingTail = true
    /// Bumped to jump to the tail and follow it again (a sent prompt).
    @State private var followRequest = 0
    @State private var actionError: String?

    private var transcript: ChatTranscript { chat.transcript }

    var body: some View {
        transcriptList
            .overlay(alignment: .top) { statusBanner }
            .actionErrorAlert($actionError)
    }

    private func run(_ operation: @escaping () async throws -> Void) {
        appModel.run(on: ref.connectionID, reporting: { actionError = $0 }, operation)
    }

    // MARK: - Transcript

    private var transcriptList: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(alignment: .leading, spacing: Spacing.md) {
                    if transcript.hasMore {
                        HStack {
                            Spacer()
                            Button("Load earlier") { loadOlder(proxy) }
                                .disabled(chat.isLoadingOlder)
                            Spacer()
                        }
                    }
                    if let error = chat.loadError {
                        loadFailure(error)
                    } else if !transcript.isLoaded {
                        loadingIndicator
                    } else if transcript.items.isEmpty, !isWorking {
                        emptyState
                    }
                    ForEach(transcript.rows) { row in
                        ChatRowView(row: row)
                            .id(row.id)
                    }
                    if isWorking {
                        HStack(spacing: Spacing.sm) {
                            ProgressView().controlSize(.small)
                            Text("Working…").foregroundStyle(.secondary)
                        }
                        .font(.callout)
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
            .onScrollGeometryChange(for: TailLayout.self, of: TailLayout.init) { _, _ in
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
        guard let anchor = transcript.rows.first?.id else { return }
        isFollowingTail = false
        run {
            try await chat.loadOlder()
            proxy.scrollTo(anchor, anchor: .top)
        }
    }

    private let tailAnchor = "tail"

    /// The sizes whose change moves the tail: the content's and the
    /// viewport's (a window resize re-pins too).
    private struct TailLayout: Equatable {
        let content: CGFloat
        let container: CGFloat
        init(_ geometry: ScrollGeometry) {
            content = geometry.contentSize.height
            container = geometry.containerSize.height
        }
    }

    /// The server drives a turn (Stop is offered), or the agent is busy under
    /// a TUI (items land at idle — the server's re-read).
    private var isWorking: Bool {
        transcript.runningTurn != nil || thread.activityState == .working
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

    /// Everything floating over the transcript's tail is Liquid Glass, and
    /// one container renders them together (separate glass views sample
    /// each other where they overlap).
    private var bottomStack: some View {
        GlassEffectContainer {
            VStack(spacing: Spacing.sm) {
                ForEach(transcript.requests, id: \.id) { request in
                    ChatRequestCard(request: request) { answer in
                        run { try await chat.answer(requestID: request.id, answer) }
                    }
                }
                if !transcript.queue.isEmpty {
                    ChatQueueStrip(prompts: transcript.queue) { prompt in
                        run { try await chat.withdraw(promptID: prompt.id) }
                    }
                }
                ChatComposer(
                    text: $draft,
                    isSending: isSending,
                    showsStop: transcript.runningTurn != nil,
                    error: chat.promptError,
                    focusRequest: focusRequest,
                    send: send,
                    stop: { run { try await chat.interrupt() } }
                )
            }
        }
        .padding(.horizontal, Spacing.xxl)
        .padding(.top, Spacing.md)
        .padding(.bottom, Spacing.lg)
        .frame(maxWidth: 820)
        .frame(maxWidth: .infinity)
    }

    private func send() {
        let prompt = draft.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !prompt.isEmpty, !isSending else { return }
        isSending = true
        Task {
            if await chat.send(prompt) {
                draft = ""
                followRequest += 1
            }
            isSending = false
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

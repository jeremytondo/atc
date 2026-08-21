// The transcript list for one Chat model: rows, "Load earlier", the
// loading/empty/error states, and the scroll behavior. The shell around it
// (window toolbar, Liquid Glass composer bar) stays with the platform app —
// this view only scrolls; bars arrive as safe-area insets.
//
// Scroll modes (ATC-215, after T3Code's timelineScrollAnchoring):
// - Tail-follow: while following, every content or viewport size change
//   re-pins the tail above the composer — done from the geometry change,
//   once layout has the new sizes. Only the user's own scroll gesture
//   leaves follow mode; ending a gesture at the tail resumes it.
// - Anchor-new-turn: when a turn starts (its prompt row lands) while
//   following, the view pins that prompt to the top of the viewport and
//   the work streams into view below it. The pin is a clamped scroll, so
//   before a viewportful of content exists below the prompt it behaves as
//   tail-follow and the prompt rises as the turn grows; the moment the
//   turn outgrows the viewport the mode flips back to tail-follow, so the
//   newest line is never hidden under the composer (T3Code's
//   "reveal end"). The anchor carries its turn: a queued prompt's turn
//   starting re-anchors instead of staying wedged, and a settled turn
//   returns the view to plain tail-follow.
// - Free: the user scrolled away; nothing moves on its own, and a
//   scroll-to-latest pill floats over the tail until they return (by
//   scrolling back, tapping the pill, or sending the next prompt).

import ATCAppServerAPI
import ATCDesign
import SwiftUI

/// An externally requested one-shot scroll to an item's row (the bar's
/// "jump" chip); every request carries its own identity, so assigning a new
/// one always jumps — no caller-side token bookkeeping.
public struct ChatJumpRequest: Equatable {
    private let id = UUID()
    let itemID: String

    public init(itemID: String) {
        self.itemID = itemID
    }
}

public struct ChatTranscriptView: View {
    let chat: ThreadChatModel
    /// The empty transcript's title (the thread's display name).
    let emptyTitle: String
    /// Bumped to jump to the tail and follow it again (a sent prompt).
    let followRequest: Int
    let jumpRequest: ChatJumpRequest?

    public init(
        chat: ThreadChatModel,
        emptyTitle: String,
        followRequest: Int,
        jumpRequest: ChatJumpRequest?
    ) {
        self.chat = chat
        self.emptyTitle = emptyTitle
        self.followRequest = followRequest
        self.jumpRequest = jumpRequest
    }

    private enum FollowMode: Equatable {
        /// Re-pin the tail on every size change.
        case tail
        /// This turn's prompt is pinned to the top (a clamped scroll) while
        /// the turn still fits in the viewport; overflow or the turn
        /// settling flips back to `.tail`, and the next turn re-anchors.
        case anchored(turnID: String)
        /// The user scrolled away; the view stays where they put it.
        case free
    }

    @State private var mode: FollowMode = .tail
    /// The live geometry's tail visibility, kept for the pill. The follow
    /// machinery itself never reads this stored copy — it acts on the
    /// value delivered with the same geometry change (see `maintainFollow`).
    @State private var isAtTail = true
    /// The pill, debounced in: eligibility must hold briefly before it
    /// shows, so a gesture passing through the tail cannot flash it.
    @State private var showsScrollToBottom = false
    /// The live scroll phase: a size change landing mid-gesture must not
    /// scroll against the user's finger (coverage starts at the gesture's
    /// first phase callback; a size change racing that single frame can
    /// still re-pin once, after which the phase wins).
    @State private var isGesturing = false

    public var body: some View {
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
                    if showsWorkingRow {
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
            .environment(
                \.chatRowActions,
                ChatRowActions(
                    retry: { [chat] prompt in
                        mode = .tail
                        proxy.scrollTo(tailAnchor, anchor: .bottom)
                        Task { await chat.send(prompt) }
                    },
                    answer: { [chat] requestID, answer in
                        chat.perform { try await chat.answer(requestID: requestID, answer) }
                    },
                    loadAttachment: { [chat] attachment in await chat.image(for: attachment) }
                )
            )
            .onScrollGeometryChange(for: ChatFollowSignal.self, of: ChatFollowSignal.init) { old, new in
                isAtTail = new.atTail
                // Only a size change moves the view; a pure offset change
                // is the user (or our own scroll) and decides nothing.
                guard new.layout != old.layout else { return }
                maintainFollow(proxy, atTail: new.atTail)
            }
            .onScrollPhaseChange { previous, phase, context in
                // A gesture leaves follow (and the anchor); a gesture ending
                // at the tail resumes following. An idle that no gesture led
                // to (the first callback, a programmatic scroll settling)
                // decides nothing.
                isGesturing = phase.isGesture
                if phase.isGesture {
                    mode = .free
                } else if phase == .idle, previous.isGesture {
                    mode = context.geometry.isAtTail ? .tail : .free
                }
            }
            .onChange(of: followRequest) { _, _ in
                mode = .tail
                proxy.scrollTo(tailAnchor, anchor: .bottom)
            }
            .onChange(of: jumpRequest) { _, jump in
                // Expanding the fold and disclosures over the item makes the
                // jump land on something visible; an off-page item is a
                // no-op rather than a lost follow mode.
                guard let jump, let target = chat.rows.reveal(itemID: jump.itemID) else { return }
                mode = .free
                withAnimation(.snappy(duration: 0.25)) {
                    proxy.scrollTo(target, anchor: .center)
                }
            }
            .onChange(of: anchorableTurnID) { _, turnID in
                // The anchor engages when a turn starts while following the
                // tail — or while still anchored on an earlier turn (a
                // queued prompt's turn re-anchors). Never from free: a new
                // turn must not yank the user out of old history.
                guard let turnID else { return }
                switch mode {
                case .free:
                    return
                case .anchored(let current) where current == turnID:
                    return
                case .tail, .anchored:
                    break
                }
                guard let prompt = promptRowID(turnID: turnID) else { return }
                mode = .anchored(turnID: turnID)
                proxy.scrollTo(prompt, anchor: .top)
            }
            .overlay(alignment: .bottom) {
                if showsScrollToBottom {
                    scrollToBottomPill(proxy)
                }
            }
            .task(id: pillEligible) {
                guard pillEligible else {
                    withAnimation(.snappy(duration: 0.15)) { showsScrollToBottom = false }
                    return
                }
                try? await Task.sleep(for: .milliseconds(150))
                guard !Task.isCancelled else { return }
                withAnimation(.snappy(duration: 0.2)) { showsScrollToBottom = true }
            }
        }
    }

    /// One size change while following: `.tail` re-pins the tail; an
    /// anchored turn keeps its prompt pinned to the top until it settles or
    /// outgrows the viewport (`atTail` is from the same geometry change, so
    /// the overflow flip lands exactly on the tick that overflowed), both
    /// of which hand back to tail-follow — the newest line must never sit
    /// hidden under the composer.
    private func maintainFollow(_ proxy: ScrollViewProxy, atTail: Bool) {
        guard !isGesturing else { return }
        switch mode {
        case .tail:
            proxy.scrollTo(tailAnchor, anchor: .bottom)
        case .anchored(let turnID):
            guard chat.runningTurn?.id == turnID, atTail,
                let prompt = promptRowID(turnID: turnID)
            else {
                mode = .tail
                proxy.scrollTo(tailAnchor, anchor: .bottom)
                return
            }
            proxy.scrollTo(prompt, anchor: .top)
        case .free:
            break
        }
    }

    /// The pill is offered whenever the user has detached and the tail is
    /// out of view — the way back to the live edge.
    private var pillEligible: Bool {
        mode == .free && !isAtTail
    }

    /// The pill measures its own residual bottom safe area and pads past
    /// it, so it floats above the shell's composer bar whether or not the
    /// overlay was inset for it.
    private func scrollToBottomPill(_ proxy: ScrollViewProxy) -> some View {
        GeometryReader { geometry in
            Button {
                mode = .tail
                withAnimation(.snappy(duration: 0.25)) {
                    proxy.scrollTo(tailAnchor, anchor: .bottom)
                }
            } label: {
                Image(systemName: "arrow.down")
                    .font(.callout.weight(.semibold))
                    .padding(Spacing.md)
            }
            .buttonStyle(.plain)
            .foregroundStyle(.primary)
            .glassEffect(in: Circle())
            .help("Scroll to the latest message")
            .accessibilityLabel("Scroll to latest")
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .bottom)
            .padding(.bottom, geometry.safeAreaInsets.bottom + Spacing.md)
        }
        .transition(.opacity.combined(with: .offset(y: 8)))
    }

    /// Older history goes in above; the row that was first stays where the
    /// eye is instead of the tail-follow yanking the view back down.
    private func loadOlder(_ proxy: ScrollViewProxy) {
        guard let anchor = chat.rows.first?.id else { return }
        mode = .free
        chat.perform { [chat] in
            try await chat.loadOlder()
            proxy.scrollTo(anchor, anchor: .top)
        }
    }

    private let tailAnchor = "tail"

    /// The server drives a turn (Stop is offered).
    private var isWorking: Bool {
        chat.runningTurn != nil
    }

    /// The tail "Working…" row shows only while nothing else is showing the
    /// same fact — once the running turn has rows on screen, its fold's
    /// spinner (or streaming answer) already says it.
    private var showsWorkingRow: Bool {
        guard let turn = chat.runningTurn else { return false }
        return !chat.rows.contains { row in
            switch row {
            case .item(let node): node.box.item.turnId == turn.id
            case .work(let work): work.turnID == turn.id
            case .turnEnded, .pending: false
            }
        }
    }

    /// The running turn once its prompt row is on screen — the
    /// anchor-new-turn trigger. Turn-start rather than send-time: a
    /// send-while-busy echo resolves into the queue strip and leaves the
    /// transcript, so there is nothing durable to anchor until the turn
    /// actually starts. Only a turn that just started qualifies — opening
    /// a thread that is already mid-turn must land at the tail, not jump
    /// up to a prompt from minutes ago. The window is symmetric so a
    /// remote server's clock running ahead cannot make an old turn look
    /// fresh; skew beyond it degrades gracefully to tail-follow.
    private var anchorableTurnID: String? {
        guard let turn = chat.runningTurn,
            let started = turn.startedAt, abs(Date.now.timeIntervalSince(started)) < 10,
            promptRowID(turnID: turn.id) != nil
        else { return nil }
        return turn.id
    }

    /// The row id of a turn's prompt (what the anchor pins).
    private func promptRowID(turnID: String) -> String? {
        chat.rows.last { row in
            guard case .item(let node) = row, case .userMessage = node.box.item else { return false }
            return node.box.item.turnId == turnID
        }?.id
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
            Text(emptyTitle).font(.title3.weight(.semibold))
            Text("Send a message to start the conversation.")
                .foregroundStyle(.secondary)
        }
        .padding(.top, Spacing.xxl)
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

/// What one geometry change delivers to the follow machinery: the layout
/// (whose change means content moved) and the tail visibility computed from
/// the very same geometry — never a stale stored copy.
struct ChatFollowSignal: Equatable {
    let layout: ChatTailLayout
    let atTail: Bool

    init(_ geometry: ScrollGeometry) {
        layout = ChatTailLayout(geometry)
        atTail = geometry.isAtTail
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

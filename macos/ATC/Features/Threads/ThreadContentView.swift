// The main content area. `TerminalPane` is ALWAYS in the hierarchy, even
// with nothing open, so live surfaces and their attach WebSockets survive
// sidebar navigation; every other state draws over it.
//
// A thread's `kind` decides what it shows (ATC-224): a chat thread draws
// `ThreadChatView` over the pane; a tui thread shows its terminal with the
// status banners over it. A tui thread whose terminal ended shows an empty state with a
// Reopen button as the whole view — never a relaunch on its own; reopening
// is just `openThread` again, and the server's open is idempotent.

import ATCAppServerAPI
import ATCAppServerTransport
import ATCDesign
import SwiftUI

struct ThreadContentView: View {
    @Environment(AppModel.self) private var appModel
    @Environment(WindowState.self) private var windowState

    var body: some View {
        ZStack {
            TerminalPane(
                visibleRef: shownTerminal,
                focusRequest: windowState.contentFocusRequest
            )
            cover
        }
    }

    /// The terminal the pane should actually show: none for a chat thread,
    /// even if a terminal is retained for it.
    private var shownTerminal: TerminalRef? {
        if case .thread(let ref) = windowState.selectedContent, appModel.thread(for: ref)?.kind != .tui {
            return nil
        }
        return windowState.visibleTerminal
    }

    @ViewBuilder
    private var cover: some View {
        switch windowState.selectedContent {
        case .dashboard:
            EmptyView()
        case .thread(let ref):
            threadCover(ref)
        case .terminal(let ref):
            terminalCover(ref)
        }
    }

    @ViewBuilder
    private func threadCover(_ ref: ThreadRef) -> some View {
        switch appModel.thread(for: ref)?.kind {
        case .none:
            unavailableCover
        case .chat:
            // Identity per thread: scroll position and the Chat hold belong
            // to one thread, never carried to the next (the composer draft
            // lives on AppModel and survives the switch).
            ThreadChatView(ref: ref).id(ref)
        case .tui:
            tuiCover(ref)
        }
    }

    /// The tui thread's states over its terminal: a failed open, a running
    /// controller, an open in flight, else nothing running.
    @ViewBuilder
    private func tuiCover(_ ref: ThreadRef) -> some View {
        if let message = windowState.threadOpenErrors[ref] {
            FloatingBanner {
                Image(systemName: "exclamationmark.triangle")
                    .foregroundStyle(.orange)
                Text(message)
                    .lineLimit(2)
                Button("Retry") {
                    Task { await windowState.openThread(ref, in: appModel) }
                }
                .keyboardShortcut(.defaultAction)
            }
        } else if let controller = controller(for: ref), !controller.hasEnded {
            TerminalStatusBanner(controller: controller)
        } else if windowState.openingThreads.contains(ref) {
            FloatingBanner {
                ProgressView().controlSize(.small)
                Text("Opening thread…")
            }
        } else {
            endedCover(ref)
        }
    }

    @ViewBuilder
    private func terminalCover(_ ref: TerminalRef) -> some View {
        if let terminal = appModel.terminal(for: ref) {
            if let controller = appModel.terminals[ref] {
                TerminalStatusBanner(controller: controller)
            } else if !terminal.isLive {
                ContentUnavailableView(
                    "Terminal Ended",
                    systemImage: "terminal",
                    description: Text(
                        "This terminal is no longer running. Delete it from the sidebar when you are done with it.")
                )
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .background(AppColors.canvas)
            }
        } else {
            unavailableCover
        }
    }

    /// A tui thread with no running terminal: the whole view is the empty
    /// state, and Reopen is the one way back (the server never relaunches).
    private func endedCover(_ ref: ThreadRef) -> some View {
        ContentUnavailableView {
            Label("Terminal Ended", systemImage: "terminal")
        } description: {
            Text("The agent's terminal is no longer running. Reopen it to continue the session.")
        } actions: {
            Button("Reopen") {
                Task { await windowState.openThread(ref, in: appModel) }
            }
            .keyboardShortcut(.defaultAction)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(AppColors.canvas)
    }

    private var unavailableCover: some View {
        ContentUnavailableView(
            "Nothing Open",
            systemImage: "bubble.left.and.text.bubble.right",
            description: Text("Open a thread from the sidebar, or start a new one.")
        )
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(AppColors.canvas)
    }

    private func controller(for ref: ThreadRef) -> TerminalSessionController? {
        windowState.threadTerminals[ref].flatMap { appModel.terminals[$0] }
    }
}

extension TerminalSessionController {
    /// The terminal itself ended (as opposed to a connection the controller
    /// can still recover).
    fileprivate var hasEnded: Bool {
        if case .ended(.terminalEnded) = phase { return true }
        return false
    }
}

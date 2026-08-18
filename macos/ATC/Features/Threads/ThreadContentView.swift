// The main content area. `TerminalPane` is ALWAYS in the hierarchy, even
// with nothing open, so live surfaces and their attach WebSockets survive
// sidebar navigation; every other state draws over it.
//
// A thread shows in one of two modes (`AppModel.viewMode(for:)`): Chat draws
// `ThreadChatView` over the pane with the thread's terminal hidden (a
// retained surface survives the mode flip untouched); TUI shows the terminal
// with the status/relaunch banners over it. A thread whose TUI terminal has
// ended keeps its final frame: the relaunch bar floats above the retained
// surface instead of replacing it, and relaunching is just `openThread`
// again — the server's open is idempotent.

import ATCAppServerAPI
import ATCAppServerTransport
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

    /// The terminal the pane should actually show: none while the selected
    /// thread is in Chat, even though its terminal stays retained.
    private var shownTerminal: TerminalRef? {
        if case .thread(let ref) = windowState.selectedContent, appModel.viewMode(for: ref) == .chat {
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
        if appModel.thread(for: ref) == nil {
            unavailableCover
        } else if appModel.viewMode(for: ref) == .chat {
            // Identity per thread: the composer draft and scroll position
            // belong to one thread, never carried to the next.
            ThreadChatView(ref: ref).id(ref)
        } else if let message = windowState.threadOpenErrors[ref] {
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
        } else if let controller = controller(for: ref) {
            if case .ended(.terminalEnded) = controller.phase {
                relaunchBar(ref)
            } else {
                TerminalStatusBanner(controller: controller)
            }
        } else if windowState.openingThreads.contains(ref) {
            FloatingBanner {
                ProgressView().controlSize(.small)
                Text("Opening thread…")
            }
        } else {
            relaunchBar(ref)
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

    private func relaunchBar(_ ref: ThreadRef) -> some View {
        FloatingBanner {
            Image(systemName: "arrow.clockwise")
            Text("Terminal ended")
            Button("Relaunch") {
                Task { await windowState.openThread(ref, in: appModel) }
            }
            .keyboardShortcut(.defaultAction)
        }
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

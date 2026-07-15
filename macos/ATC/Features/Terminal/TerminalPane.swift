import SwiftUI

/// All live terminal surfaces, stacked; only the selected one is visible.
/// Hidden surfaces stay in the hierarchy so switching sessions never tears
/// down a surface or drops its WebSocket.
struct TerminalPane: View {
    @Environment(AppModel.self) private var appModel
    let visibleRef: SessionRef?
    let focusRequest: UInt
    @FocusState private var focusedTerminal: SessionRef?

    var body: some View {
        ZStack {
            // Catppuccin Mocha base: pairs with the Ghostty surface theme,
            // not app chrome — keep this literal out of the design tokens.
            Color(red: 0.117, green: 0.117, blue: 0.180)
            ForEach(refs, id: \.self) { ref in
                if let controller = appModel.terminals[ref] {
                    TerminalHostView(
                        ref: ref,
                        controller: controller,
                        focus: $focusedTerminal
                    )
                        .opacity(ref == visibleRef ? 1 : 0)
                        .allowsHitTesting(ref == visibleRef)
                }
            }
        }
        .onChange(of: focusTarget, initial: true) {
            focusedTerminal = focusTarget.ref
        }
    }

    /// Includes attachment availability so a just-created or explicitly
    /// reconnected controller gets focus as soon as its surface exists.
    private var focusTarget: TerminalFocusTarget {
        TerminalFocusTarget(
            ref: visibleRef.flatMap { appModel.terminals[$0] == nil ? nil : $0 },
            request: focusRequest
        )
    }

    private var refs: [SessionRef] {
        appModel.terminals.keys.sorted {
            ($0.sessionID, $0.connectionID.uuidString) < ($1.sessionID, $1.connectionID.uuidString)
        }
    }
}

private struct TerminalFocusTarget: Equatable {
    let ref: SessionRef?
    let request: UInt
}

/// Phase-driven banner shown over the terminal.
struct TerminalStatusBanner: View {
    let controller: TerminalSessionController
    /// Removes the controller (used once a session is gone for good).
    var onDismiss: () -> Void

    var body: some View {
        switch controller.phase {
        case .connecting:
            banner {
                ProgressView().controlSize(.small)
                Text("Connecting…")
            }
        case .connected:
            EmptyView()
        case .reconnecting:
            banner {
                ProgressView().controlSize(.small)
                Text("Reconnecting…")
            }
        case .ended(.sessionEnded):
            banner {
                Image(systemName: "checkmark.circle")
                Text("Session ended")
                Button("Dismiss") { onDismiss() }
            }
        case .ended(.serverError):
            banner {
                Image(systemName: "exclamationmark.triangle")
                Text("Server error")
                Button("Reconnect") { controller.reconnect() }
            }
        case .ended(.transportFailure):
            banner {
                Image(systemName: "wifi.exclamationmark")
                Text("Disconnected")
                Button("Reconnect") { controller.reconnect() }
            }
        case .ended(.closedByClient):
            EmptyView()
        }
    }

    private func banner(@ViewBuilder content: () -> some View) -> some View {
        HStack(spacing: Spacing.sm, content: content)
            .padding(.horizontal, Spacing.md)
            .padding(.vertical, Spacing.sm)
            // The app's one floating overlay — the only Liquid Glass call
            // outside what system components provide for free.
            .glassEffect()
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
            .padding(.top, Spacing.lg)
    }
}

import SwiftUI

/// All live terminal surfaces, stacked; only the selected one is visible.
/// Hidden surfaces stay in the hierarchy so switching sessions never tears
/// down a surface or drops its WebSocket.
struct TerminalPane: View {
    @Environment(AppModel.self) private var appModel
    let visibleRef: SessionRef?
    let focusRequest: UInt
    let preferences: TerminalPreferences

    var body: some View {
        ZStack {
            let backingColor = TerminalPresentation.backingColor(preferences: preferences)
            Color(
                red: backingColor.red,
                green: backingColor.green,
                blue: backingColor.blue
            )
            ForEach(refs, id: \.self) { ref in
                if let controller = appModel.terminals[ref] {
                    TerminalHostView(
                        controller: controller,
                        isVisible: ref == visibleRef,
                        focusRequest: focusRequest
                    )
                }
            }
        }
    }

    private var refs: [SessionRef] {
        appModel.terminals.keys.sorted {
            ($0.sessionID, $0.connectionID.uuidString) < ($1.sessionID, $1.connectionID.uuidString)
        }
    }
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
            // Floating overlay on Liquid Glass, like the toolbar's
            // workspace pill.
            .glassEffect()
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
            .padding(.top, Spacing.lg)
    }
}

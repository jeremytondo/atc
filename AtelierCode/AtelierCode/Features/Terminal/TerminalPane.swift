import SwiftUI

/// All live terminal surfaces, stacked; only the selected one is visible.
/// Hidden surfaces stay in the hierarchy so switching sessions never tears
/// down a surface or drops its WebSocket.
struct TerminalPane: View {
    @Environment(AppModel.self) private var appModel
    let visibleSessionID: String?
    @FocusState private var focusedTerminal: String?

    var body: some View {
        ZStack {
            Color(red: 0.117, green: 0.117, blue: 0.180) // Mocha base, behind surface padding
            ForEach(controllers) { controller in
                TerminalHostView(controller: controller, focus: $focusedTerminal)
                    .opacity(controller.sessionID == visibleSessionID ? 1 : 0)
                    .allowsHitTesting(controller.sessionID == visibleSessionID)
            }
        }
        .onChange(of: visibleSessionID, initial: true) {
            focusedTerminal = visibleSessionID
        }
    }

    private var controllers: [TerminalSessionController] {
        appModel.terminals.values.sorted { $0.sessionID < $1.sessionID }
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
        HStack(spacing: 8, content: content)
            .padding(.horizontal, 14)
            .padding(.vertical, 8)
            .background(.regularMaterial, in: Capsule())
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
            .padding(.top, 16)
    }
}

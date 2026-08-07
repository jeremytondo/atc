import SwiftUI
import ATCAppServerTransport

/// All retained terminal surfaces, stacked; only the selected one is visible.
/// Hidden surfaces stay in the hierarchy so switching threads never tears
/// down a surface or drops its attach WebSocket.
struct TerminalPane: View {
    @Environment(AppModel.self) private var appModel
    let visibleRef: TerminalRef?
    let focusRequest: UInt

    var body: some View {
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

    private var refs: [TerminalRef] {
        appModel.terminals.keys.sorted {
            ($0.terminalID, $0.connectionID.uuidString) < ($1.terminalID, $1.connectionID.uuidString)
        }
    }
}

/// Phase-driven banner floating over the terminal. An authoritative end is
/// deliberately quiet here — the thread content area owns that state, because
/// only it can offer the relaunch.
struct TerminalStatusBanner: View {
    let controller: TerminalSessionController

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
        case .ended(.terminalEnded):
            banner {
                Image(systemName: "checkmark.circle")
                Text("Terminal ended")
            }
        case .ended(.rejected(let statusCode)):
            banner {
                Image(systemName: "exclamationmark.triangle")
                Text("Attach refused (\(statusCode))")
                Button("Reconnect") { controller.reconnect() }
            }
        case .ended(.retryable):
            banner {
                Image(systemName: "wifi.exclamationmark")
                Text("Connection lost")
                Button("Reconnect") { controller.reconnect() }
            }
        case .ended(.closedByClient):
            banner {
                Image(systemName: "cable.connector.slash")
                Text("Disconnected")
                Button("Reconnect") { controller.reconnect() }
            }
        }
    }

    private func banner(@ViewBuilder content: () -> some View) -> some View {
        HStack(spacing: Spacing.sm, content: content)
            .padding(.horizontal, Spacing.md)
            .padding(.vertical, Spacing.sm)
            // Floating overlay on Liquid Glass, like the toolbar controls.
            .glassEffect()
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
            .padding(.top, Spacing.lg)
    }
}

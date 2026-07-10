import SwiftUI
import GhosttyTerminal

/// The one view that hosts a Ghostty surface. Keeping the GhosttyTerminal
/// dependency contained here (plus the controller/bridge) keeps a future
/// swap to a source-built GhosttyKit cheap.
struct TerminalHostView: View {
    let controller: TerminalSessionController
    var focus: FocusState<String?>.Binding

    var body: some View {
        TerminalSurfaceView(context: controller.viewState)
            .terminalFocused(focus, equals: controller.sessionID)
    }
}

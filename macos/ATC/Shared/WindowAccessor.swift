// The SwiftUI→AppKit seam for anything that must own state on the hosting
// NSWindow (event monitors, first-responder capture). SwiftUI reports the
// window only through a hosted view, and the same three-step plumbing —
// attach on `viewDidMoveToWindow`, re-attach when the window changes under an
// update, tear down on dismantle — is needed by every such accessor, so it
// lives here once and the attachments only implement their own policy.

import AppKit
import SwiftUI

/// Window-owned state installed by a `WindowAccessor`, held as the
/// representable's coordinator.
@MainActor
protocol WindowAttachment: AnyObject {
    /// The window currently attached to, so the accessor can notice a move.
    var hostWindow: NSWindow? { get }
    /// Attach to `window` (nil while the view is unhosted). Called for every
    /// window change; implementations decide what a repeat means.
    func attach(to window: NSWindow?)
    /// The view is going away: release everything installed on the window.
    func detach()
}

struct WindowAccessor<Attachment: WindowAttachment>: NSViewRepresentable {
    let makeAttachment: @MainActor () -> Attachment
    /// Refreshes the attachment's captured closures on every SwiftUI update,
    /// which is what keeps a long-lived coordinator from calling into a stale
    /// view generation.
    let update: @MainActor (Attachment) -> Void

    func makeCoordinator() -> Attachment {
        makeAttachment()
    }

    func makeNSView(context: Context) -> HostView {
        let view = HostView()
        view.onWindowChange = { [weak coordinator = context.coordinator] window in
            coordinator?.attach(to: window)
        }
        return view
    }

    func updateNSView(_ nsView: HostView, context: Context) {
        update(context.coordinator)
        if nsView.window !== context.coordinator.hostWindow {
            context.coordinator.attach(to: nsView.window)
        }
    }

    static func dismantleNSView(_ nsView: HostView, coordinator: Attachment) {
        nsView.onWindowChange = nil
        coordinator.detach()
    }

    final class HostView: NSView {
        var onWindowChange: ((NSWindow?) -> Void)?

        override func viewDidMoveToWindow() {
            super.viewDidMoveToWindow()
            onWindowChange?(window)
        }
    }
}

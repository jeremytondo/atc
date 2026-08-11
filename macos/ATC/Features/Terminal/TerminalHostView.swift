import AppKit
import OSLog
import SwiftUI
import GhosttyTerminal

private let logger = Logger(subsystem: "ElevenIdeas.atc", category: "terminal")

/// The one view that hosts a Ghostty surface. Each retained terminal owns an
/// AppKit container so focus can be transferred directly between the actual
/// terminal input views without shared SwiftUI focus state.
struct TerminalHostView: NSViewRepresentable {
    let controller: TerminalSessionController
    let isVisible: Bool
    let focusRequest: UInt

    func makeNSView(context: Context) -> TerminalContainerView {
        TerminalContainerView(controller: controller)
    }

    func updateNSView(_ view: TerminalContainerView, context: Context) {
        view.update(
            controller: controller,
            isVisible: isVisible,
            focusRequest: focusRequest
        )
    }

    static func dismantleNSView(_ view: TerminalContainerView, coordinator: ()) {
        view.tearDown()
    }
}

/// Owns exactly one Ghostty view hierarchy. Scoping the first-responder lookup
/// to this container prevents one retained terminal from focusing another.
final class TerminalContainerView: NSView {
    private static let focusRetryDelay = Duration.milliseconds(10)
    private static let focusRetryLimit = 100

    private let hostingView: NSHostingView<TerminalSurfaceView>
    private var wasVisible = false
    private var lastFocusRequest: UInt?
    private var focusTask: Task<Void, Never>?
    private var focusResignTask: Task<Void, Never>?
    private var controllerID: ObjectIdentifier
    private var acceptsPointerInput = false

    init(controller: TerminalSessionController) {
        hostingView = NSHostingView(
            rootView: TerminalSurfaceView(context: controller.viewState)
        )
        controllerID = ObjectIdentifier(controller)
        super.init(frame: .zero)
        alphaValue = 0

        hostingView.translatesAutoresizingMaskIntoConstraints = false
        addSubview(hostingView)
        NSLayoutConstraint.activate([
            hostingView.leadingAnchor.constraint(equalTo: leadingAnchor),
            hostingView.trailingAnchor.constraint(equalTo: trailingAnchor),
            hostingView.topAnchor.constraint(equalTo: topAnchor),
            hostingView.bottomAnchor.constraint(equalTo: bottomAnchor),
        ])
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) {
        fatalError("init(coder:) has not been implemented")
    }

    func update(
        controller: TerminalSessionController,
        isVisible: Bool,
        focusRequest: UInt
    ) {
        let newControllerID = ObjectIdentifier(controller)
        if controllerID != newControllerID {
            controllerID = newControllerID
            hostingView.rootView = TerminalSurfaceView(context: controller.viewState)
            // A replaced controller brings a fresh surface; treat it as a
            // first appearance so a visible terminal regains focus.
            wasVisible = false
        }

        let becameHidden = wasVisible && !isVisible
        let shouldFocus = isVisible && (!wasVisible || lastFocusRequest != focusRequest)
        wasVisible = isVisible
        lastFocusRequest = focusRequest
        acceptsPointerInput = isVisible
        alphaValue = isVisible ? 1 : 0

        if !isVisible {
            focusTask?.cancel()
            focusTask = nil
            // Only the visible→hidden transition can leave this terminal as
            // the stale first responder; steady-state hidden updates would
            // just schedule no-op work.
            if becameHidden { scheduleTerminalFocusResignation() }
        } else if shouldFocus {
            focusResignTask?.cancel()
            focusResignTask = nil
            // Visibility and focus are intentionally ordered in the same
            // AppKit update. The transparent retained views remain mounted,
            // so switching back never waits for SwiftUI to rebuild Ghostty.
            requestTerminalFocus()
        }
    }

    override func hitTest(_ point: NSPoint) -> NSView? {
        guard acceptsPointerInput else { return nil }
        return super.hitTest(point)
    }

    /// SwiftUI can materialize the hosted AppKit terminal a few run-loop
    /// turns after this representable becomes visible. Keep retrying until
    /// that concrete input view exists, bounded so a broken hierarchy cannot
    /// retry indefinitely.
    private func requestTerminalFocus() {
        focusTask?.cancel()
        focusTask = Task { [weak self] in
            for _ in 0..<Self.focusRetryLimit {
                try? await Task.sleep(for: Self.focusRetryDelay)
                guard !Task.isCancelled, let self else { return }
                if self.takeFocus() { return }
            }
            logger.error(
                "Abandoned terminal focus transfer after \(Self.focusRetryLimit) attempts; the surface never produced an input view"
            )
        }
    }

    /// True once this container's terminal input view exists and holds first
    /// responder.
    private func takeFocus() -> Bool {
        hostingView.layoutSubtreeIfNeeded()
        guard let window, let terminalView = terminalInputView() else { return false }
        return window.firstResponder === terminalView || window.makeFirstResponder(terminalView)
    }

    func tearDown() {
        focusTask?.cancel()
        focusTask = nil
        scheduleTerminalFocusResignation()
    }

    private func scheduleTerminalFocusResignation() {
        focusResignTask?.cancel()
        guard let window, let terminalView = terminalInputView() else { return }
        focusResignTask = Task { [weak window, weak terminalView] in
            try? await Task.sleep(for: Self.focusRetryDelay)
            guard !Task.isCancelled, let window, let terminalView,
                  window.firstResponder === terminalView
            else { return }
            window.makeFirstResponder(nil)
        }
    }

    /// This hosting hierarchy contains exactly one Ghostty surface. Matching
    /// its concrete input view type (rather than any first-responder-capable
    /// view) keeps the transfer correct even if SwiftUI hosting or the
    /// surface ever grows other focusable descendants.
    private func terminalInputView() -> NSView? {
        hostingView.firstDescendant { $0 is TerminalView }
    }
}

private extension NSView {
    func firstDescendant(matching predicate: (NSView) -> Bool) -> NSView? {
        for subview in subviews {
            if predicate(subview) { return subview }
            if let match = subview.firstDescendant(matching: predicate) {
                return match
            }
        }
        return nil
    }
}

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
    private static let focusRetryDelay = 0.01
    private static let focusRetryLimit = 100

    private let hostingView: NSHostingView<TerminalSurfaceView>
    private var wasVisible = false
    private var lastFocusRequest: UInt?
    private var focusRetryTimer: Timer?
    private var focusResignTimer: Timer?
    private var focusAttempt = 0
    private var wantsFocus = false
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
            cancelPendingFocus()
            // Only the visible→hidden transition can leave this terminal as
            // the stale first responder; steady-state hidden updates would
            // just schedule no-op timers.
            if becameHidden { scheduleTerminalFocusResignation() }
        } else if shouldFocus {
            focusResignTimer?.invalidate()
            focusResignTimer = nil
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

    override func viewDidMoveToWindow() {
        super.viewDidMoveToWindow()
        if wantsFocus { schedulePendingFocus() }
    }

    private func requestTerminalFocus() {
        wantsFocus = true
        focusAttempt = 0
        schedulePendingFocus()
    }

    private func cancelPendingFocus() {
        wantsFocus = false
        focusRetryTimer?.invalidate()
        focusRetryTimer = nil
    }

    private func schedulePendingFocus() {
        guard wantsFocus else { return }
        focusRetryTimer?.invalidate()
        let timer = Timer(timeInterval: Self.focusRetryDelay, repeats: false) { [weak self] _ in
            MainActor.assumeIsolated {
                self?.applyPendingFocus()
            }
        }
        focusRetryTimer = timer
        RunLoop.main.add(timer, forMode: .common)
    }

    private func applyPendingFocus() {
        guard wantsFocus else { return }
        hostingView.layoutSubtreeIfNeeded()

        guard let window, let terminalView = terminalInputView() else {
            retryPendingFocus()
            return
        }

        if window.firstResponder === terminalView || window.makeFirstResponder(terminalView) {
            wantsFocus = false
            focusRetryTimer = nil
        } else {
            retryPendingFocus()
        }
    }

    /// SwiftUI can materialize the hosted AppKit terminal a few run-loop
    /// turns after this representable becomes visible. Keep the request
    /// alive until that concrete input view exists, with a bounded retry so
    /// a broken hierarchy cannot schedule work indefinitely.
    private func retryPendingFocus() {
        focusAttempt += 1
        guard focusAttempt < Self.focusRetryLimit else {
            wantsFocus = false
            focusRetryTimer = nil
            logger.error(
                "Abandoned terminal focus transfer after \(Self.focusRetryLimit) attempts; the surface never produced an input view"
            )
            return
        }
        schedulePendingFocus()
    }

    func tearDown() {
        cancelPendingFocus()
        scheduleTerminalFocusResignation()
    }

    private func scheduleTerminalFocusResignation() {
        focusResignTimer?.invalidate()
        guard let window, let terminalView = terminalInputView() else { return }
        let timer = Timer(timeInterval: Self.focusRetryDelay, repeats: false) {
            [weak window, weak terminalView] _ in
            MainActor.assumeIsolated {
                guard let window, let terminalView,
                      window.firstResponder === terminalView
                else { return }
                window.makeFirstResponder(nil)
            }
        }
        focusResignTimer = timer
        RunLoop.main.add(timer, forMode: .common)
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

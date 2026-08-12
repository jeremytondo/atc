import AppKit
import SwiftUI

struct NativeShortcutDispatch {
    let perform: @MainActor (AppAction, NSWindow) -> Void

    static let live = NativeShortcutDispatch { action, window in
        switch action {
        case .terminate:
            NSApp.terminate(nil)
        case .closeWindow:
            window.performClose(nil)
        case .closeAllWindows:
            // Only user-closable windows: performClose beeps on panels and
            // helper windows without a close button. Key window last so
            // focus does not hop mid-sweep.
            let closable = NSApp.windows.filter {
                $0 !== window && $0.isVisible && $0.styleMask.contains(.closable)
            }
            for otherWindow in closable {
                otherWindow.performClose(nil)
            }
            if window.styleMask.contains(.closable) {
                window.performClose(nil)
            }
        }
    }
}

struct KeyboardMonitorHost: View {
    let router: WindowKeyboardRouter
    let onDeactivate: () -> Void
    let focusFallback: () -> Void

    var body: some View {
        WindowAccessor {
            Coordinator(
                router: router,
                onDeactivate: onDeactivate,
                focusFallback: focusFallback
            )
        } update: { coordinator in
            coordinator.router = router
            coordinator.onDeactivate = onDeactivate
            coordinator.focusFallback = focusFallback
        }
    }

    @MainActor
    final class Coordinator: WindowAttachment {
        var router: WindowKeyboardRouter
        var onDeactivate: () -> Void
        var focusFallback: () -> Void
        let nativeShortcutDispatch: NativeShortcutDispatch
        private(set) weak var hostWindow: NSWindow?
        private var monitor: Any?
        private var flagsMonitor: Any?
        private var observers: [NSObjectProtocol] = []

        init(
            router: WindowKeyboardRouter,
            onDeactivate: @escaping () -> Void,
            focusFallback: @escaping () -> Void,
            nativeShortcutDispatch: NativeShortcutDispatch = .live
        ) {
            self.router = router
            self.onDeactivate = onDeactivate
            self.focusFallback = focusFallback
            self.nativeShortcutDispatch = nativeShortcutDispatch
        }

        func attach(to window: NSWindow?) {
            guard window !== hostWindow || monitor == nil else { return }
            detach()
            guard let window else { return }
            hostWindow = window
            monitor = NSEvent.addLocalMonitorForEvents(matching: .keyDown) {
                [weak self, weak window] event in
                guard let self, let window,
                      event.window === window,
                      window.isKeyWindow
                else { return event }
                return self.handleKeyDown(event: event, in: window)
            }

            // Held-modifier state for the sidebar's ⌘/⌥⌘ jump badges. Never
            // consumed; only the key window's monitor records, so a
            // background window's badges cannot light up.
            flagsMonitor = NSEvent.addLocalMonitorForEvents(matching: .flagsChanged) {
                [weak self, weak window] event in
                guard let self, let window, window.isKeyWindow else { return event }
                self.router.heldModifiers = KeyStroke.Modifiers(event.modifierFlags)
                return event
            }

            let center = NotificationCenter.default
            // Becoming key reseeds from the live hardware state: flags
            // changed while another window was key (⌘Tab back with ⌘ still
            // down) never produced a flagsChanged event here.
            observers.append(center.addObserver(
                forName: NSWindow.didBecomeKeyNotification,
                object: window,
                queue: .main
            ) { [weak self] _ in
                MainActor.assumeIsolated {
                    self?.router.heldModifiers = KeyStroke.Modifiers(NSEvent.modifierFlags)
                }
            })
            observers.append(center.addObserver(
                forName: NSWindow.didResignKeyNotification,
                object: window,
                queue: .main
            ) { [weak self] _ in
                MainActor.assumeIsolated {
                    self?.router.cancel()
                    self?.router.heldModifiers = []
                    self?.onDeactivate()
                    self?.restoreOrphanedResponder()
                }
            })
            observers.append(center.addObserver(
                forName: NSApplication.didResignActiveNotification,
                object: NSApp,
                queue: .main
            ) { [weak self] _ in
                MainActor.assumeIsolated {
                    self?.router.cancel()
                    self?.router.heldModifiers = []
                    self?.onDeactivate()
                    self?.restoreOrphanedResponder()
                }
            })
        }

        // Seam below the window guards lets tests use synthesized events
        // without installing a process-wide monitor.
        func handleKeyDown(event: NSEvent, in window: NSWindow) -> NSEvent? {
            guard let stroke = KeyStroke.normalize(event: event) else { return event }
            if let action = NativeShortcuts.appAction(for: stroke) {
                if !event.isARepeat {
                    nativeShortcutDispatch.perform(action, window)
                }
                return nil
            }

            let wasSuspended = router.isSuspended()
            let handled = router.handle(stroke, isRepeat: event.isARepeat)
            // The palette opener flips suspension synchronously, but the
            // palette's focus accessor only mounts on the next SwiftUI
            // commit; keystrokes already queued behind the opener would
            // land in the still-focused terminal. Clearing focus at the
            // flip closes that gap, stashing the responder so dismissal
            // can still restore it.
            if handled, !wasSuspended, router.isSuspended() {
                router.responderBeforeSuspension = window.firstResponder
                window.makeFirstResponder(nil)
            }
            return handled ? nil : event
        }

        // Dismissal normally restores focus through the palette's window
        // accessor, but deactivation can dismiss the palette before the
        // accessor's first mount consumes the stash; the responder captured
        // at the suspension flip would then stay lost.
        private func restoreOrphanedResponder() {
            guard let stashed = router.responderBeforeSuspension else { return }
            router.responderBeforeSuspension = nil
            if let window = hostWindow,
               let view = stashed as? NSView,
               view.window === window,
               view.acceptsFirstResponder,
               window.makeFirstResponder(view) {
                return
            }
            focusFallback()
        }

        func detach() {
            if let monitor {
                NSEvent.removeMonitor(monitor)
                self.monitor = nil
            }
            if let flagsMonitor {
                NSEvent.removeMonitor(flagsMonitor)
                self.flagsMonitor = nil
            }
            for observer in observers {
                NotificationCenter.default.removeObserver(observer)
            }
            observers.removeAll()
            hostWindow = nil
            router.cancel()
            router.heldModifiers = []
        }

        // Isolated so the safety-net cleanup can touch main-actor state; the
        // coordinator's last reference is always dropped on the main actor,
        // so this never actually schedules a hop.
        isolated deinit {
            if let monitor { NSEvent.removeMonitor(monitor) }
            if let flagsMonitor { NSEvent.removeMonitor(flagsMonitor) }
            for observer in observers {
                NotificationCenter.default.removeObserver(observer)
            }
        }
    }
}

struct KeyboardRoutingContainer<Content: View>: View {
    /// Built once by the per-window root (WindowRootView) — this container
    /// stores inputs only, so window-root body passes allocate nothing.
    let router: WindowKeyboardRouter
    let windowState: WindowState
    let configStore: ConfigurationStore
    @ViewBuilder let content: Content

    init(
        router: WindowKeyboardRouter,
        windowState: WindowState,
        configStore: ConfigurationStore,
        @ViewBuilder content: () -> Content
    ) {
        self.router = router
        self.windowState = windowState
        self.configStore = configStore
        self.content = content()
    }

    var body: some View {
        content
            .overlay {
                if windowState.commandPalettePresentation != nil {
                    CommandPaletteView()
                }
            }
            .overlay {
                CommandFeedbackOverlay()
            }
            .environment(configStore)
            .environment(router)
            .background(KeyboardMonitorHost(
                router: router,
                onDeactivate: { windowState.commandPalettePresentation = nil },
                focusFallback: { windowState.requestTerminalFocus() }
            ))
            .onChange(of: configStore.configuration.keymap.generation, initial: true) {
                router.keymap = configStore.configuration.keymap
            }
            .onChange(of: windowState.commandPalettePresentation) {
                if windowState.commandPalettePresentation != nil {
                    router.cancel()
                }
            }
    }
}

extension KeyStroke.Modifiers {
    /// The four routable modifiers from an event's flags. Caps Lock and Fn
    /// are deliberately dropped so they never break an exact match.
    init(_ flags: NSEvent.ModifierFlags) {
        self = []
        let device = flags.intersection(.deviceIndependentFlagsMask)
        if device.contains(.command) { insert(.command) }
        if device.contains(.control) { insert(.control) }
        if device.contains(.option) { insert(.option) }
        if device.contains(.shift) { insert(.shift) }
    }
}

extension KeyStroke {
    static func normalize(event: NSEvent) -> KeyStroke? {
        let modifiers = Modifiers(event.modifierFlags)

        // Escape normalizes to the bare stroke regardless of held modifiers
        // so a pending sequence always cancels silently.
        if event.keyCode == 53 {
            return .escape
        }
        guard let characters = event.characters(byApplyingModifiers: [])
                ?? event.charactersIgnoringModifiers,
              characters.count == 1,
              let scalar = characters.lowercased().unicodeScalars.first,
              isPrintable(scalar)
        else { return nil }
        return KeyStroke(key: characters.lowercased(), modifiers: modifiers)
    }
}

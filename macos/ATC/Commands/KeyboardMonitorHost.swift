import AppKit
import SwiftUI

struct KeyboardMonitorHost: NSViewRepresentable {
    let router: WindowKeyboardRouter
    let onDeactivate: () -> Void

    func makeCoordinator() -> Coordinator {
        Coordinator(router: router, onDeactivate: onDeactivate)
    }

    func makeNSView(context: Context) -> HostView {
        let view = HostView()
        view.onWindowChange = { [weak coordinator = context.coordinator] window in
            coordinator?.install(for: window)
        }
        return view
    }

    func updateNSView(_ nsView: HostView, context: Context) {
        context.coordinator.router = router
        context.coordinator.onDeactivate = onDeactivate
        if nsView.window !== context.coordinator.hostWindow {
            context.coordinator.install(for: nsView.window)
        }
    }

    static func dismantleNSView(_ nsView: HostView, coordinator: Coordinator) {
        nsView.onWindowChange = nil
        coordinator.stop()
    }

    final class HostView: NSView {
        var onWindowChange: ((NSWindow?) -> Void)?

        override func viewDidMoveToWindow() {
            super.viewDidMoveToWindow()
            onWindowChange?(window)
        }
    }

    @MainActor
    final class Coordinator {
        var router: WindowKeyboardRouter
        var onDeactivate: () -> Void
        private(set) weak var hostWindow: NSWindow?
        private var monitor: Any?
        private var observers: [NSObjectProtocol] = []

        init(router: WindowKeyboardRouter, onDeactivate: @escaping () -> Void) {
            self.router = router
            self.onDeactivate = onDeactivate
        }

        func install(for window: NSWindow?) {
            guard window !== hostWindow || monitor == nil else { return }
            stop()
            guard let window else { return }
            hostWindow = window
            monitor = NSEvent.addLocalMonitorForEvents(matching: .keyDown) {
                [weak self, weak window] event in
                guard let self, let window,
                      event.window === window,
                      window.isKeyWindow,
                      let stroke = KeyStroke.normalize(event: event)
                else { return event }
                return self.router.handle(stroke, isRepeat: event.isARepeat) ? nil : event
            }

            let center = NotificationCenter.default
            observers.append(center.addObserver(
                forName: NSWindow.didResignKeyNotification,
                object: window,
                queue: .main
            ) { [weak self] _ in
                MainActor.assumeIsolated {
                    self?.router.cancel()
                    self?.onDeactivate()
                }
            })
            observers.append(center.addObserver(
                forName: NSApplication.didResignActiveNotification,
                object: NSApp,
                queue: .main
            ) { [weak self] _ in
                MainActor.assumeIsolated {
                    self?.router.cancel()
                    self?.onDeactivate()
                }
            })
        }

        func stop() {
            if let monitor {
                NSEvent.removeMonitor(monitor)
                self.monitor = nil
            }
            for observer in observers {
                NotificationCenter.default.removeObserver(observer)
            }
            observers.removeAll()
            hostWindow = nil
            router.cancel()
        }

        deinit {
            if let monitor { NSEvent.removeMonitor(monitor) }
            for observer in observers {
                NotificationCenter.default.removeObserver(observer)
            }
        }
    }
}

struct KeyboardRoutingContainer<Content: View>: View {
    let appModel: AppModel
    let windowState: WindowState
    let configStore: KeyboardConfigStore
    @ViewBuilder let content: Content

    @State private var router: WindowKeyboardRouter

    init(
        appModel: AppModel,
        windowState: WindowState,
        configStore: KeyboardConfigStore,
        @ViewBuilder content: () -> Content
    ) {
        self.appModel = appModel
        self.windowState = windowState
        self.configStore = configStore
        self.content = content()
        let context = CommandContext(
            appModel: appModel,
            windowState: windowState,
            configStore: configStore
        )
        let router = WindowKeyboardRouter(
            keymap: configStore.keymap,
            context: context
        )
        router.isSuspended = { windowState.isCommandPalettePresented }
        _router = State(initialValue: router)
    }

    var body: some View {
        content
            .overlay {
                if windowState.isCommandPalettePresented {
                    CommandPaletteView()
                }
            }
            .overlay {
                CommandFeedbackOverlay()
            }
            .environment(configStore)
            .environment(router)
            .background(KeyboardMonitorHost(router: router) {
                windowState.isCommandPalettePresented = false
            })
            .onChange(of: configStore.keymap.generation, initial: true) {
                router.keymap = configStore.keymap
            }
            .onChange(of: windowState.isCommandPalettePresented) {
                if windowState.isCommandPalettePresented {
                    router.cancel()
                }
            }
    }
}

extension KeyStroke {
    static func normalize(event: NSEvent) -> KeyStroke? {
        let flags = event.modifierFlags.intersection(.deviceIndependentFlagsMask)
        var modifiers: Modifiers = []
        if flags.contains(.command) { modifiers.insert(.command) }
        if flags.contains(.control) { modifiers.insert(.control) }
        if flags.contains(.option) { modifiers.insert(.option) }
        if flags.contains(.shift) { modifiers.insert(.shift) }

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

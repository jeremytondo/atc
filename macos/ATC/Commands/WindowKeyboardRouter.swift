import Foundation
import Observation

struct RouterFlash: Equatable {
    let message: String
}

@MainActor
@Observable
final class WindowKeyboardRouter {
    enum State {
        case idle
        case pending(node: [KeyStroke: ResolvedKeymap.Node])
    }

    private(set) var state: State = .idle
    private(set) var flash: RouterFlash?
    /// Modifiers physically held right now, as the flags-changed monitor
    /// reports them; drives the sidebar's ⌘/⌥⌘ shortcut badges. The monitor
    /// host resets this on window resign (⌘Tab-ing away never leaves badges
    /// stuck on) and reseeds it from the hardware state on become-key.
    var heldModifiers: KeyStroke.Modifiers = []
    var keymap: ResolvedKeymap {
        didSet {
            if oldValue.generation != keymap.generation {
                cancel()
            }
        }
    }

    @ObservationIgnored var isSuspended: @MainActor () -> Bool = { false }
    /// The responder that held focus when suspension began. The key monitor
    /// stashes it before clearing window focus so the palette can restore it
    /// on dismissal; AnyObject keeps AppKit out of this file.
    @ObservationIgnored weak var responderBeforeSuspension: AnyObject?
    /// Sidebar jump dispatch, injected by the routing container so this file
    /// stays free of navigation concerns.
    @ObservationIgnored var performJump: @MainActor (SidebarJump) -> Void = { _ in }
    @ObservationIgnored private let executeCommand: @MainActor (CommandID) -> CommandAvailability
    @ObservationIgnored private var flashTask: Task<Void, Never>?
    @ObservationIgnored private var flashToken = 0

    init(keymap: ResolvedKeymap, context: CommandContext) {
        self.keymap = keymap
        self.executeCommand = { CommandRegistry.execute($0, context: context) }
    }

    init(
        keymap: ResolvedKeymap,
        execute: @escaping @MainActor (CommandID) -> CommandAvailability
    ) {
        self.keymap = keymap
        self.executeCommand = execute
    }

    var pendingNode: [KeyStroke: ResolvedKeymap.Node]? {
        guard case .pending(let node) = state else { return nil }
        return node
    }

    @discardableResult
    func handle(_ stroke: KeyStroke, isRepeat: Bool) -> Bool {
        guard !isSuspended() else { return false }
        switch state {
        case .idle:
            // ⌘1–9 / ⌥⌘1–9 are hardcoded, reserved ahead of the keymap (the
            // resolver refuses user bindings on them), and always consumed:
            // a digit without a target does nothing rather than falling
            // through to the focused terminal. A pending sequence keeps
            // sequence semantics instead, like every other direct binding.
            if let jump = SidebarShortcuts.jump(for: stroke) {
                clearFlash()
                if !isRepeat { performJump(jump) }
                return true
            }
            guard let node = keymap.root[stroke] else { return false }
            if isRepeat { return true }
            return handle(node)
        case .pending(let continuations):
            if isRepeat { return true }
            if stroke == .escape {
                cancel()
                return true
            }
            guard let node = continuations[stroke] else {
                cancel()
                showFlash("No matching command")
                return true
            }
            return handle(node)
        }
    }

    func cancel() {
        state = .idle
    }

    func showUnavailable(reason: String) {
        showFlash(reason)
    }

    private func handle(_ node: ResolvedKeymap.Node) -> Bool {
        // A recognized binding supersedes lingering feedback; without this a
        // fresh flash would hide the hint of a sequence started within 800 ms.
        clearFlash()
        switch node {
        case .command(let command):
            cancel()
            if case .unavailable(let reason) = executeCommand(command) {
                showFlash(reason)
            }
        case .prefix(let continuations):
            state = .pending(node: continuations)
        }
        return true
    }

    private func clearFlash() {
        flashToken += 1
        flashTask?.cancel()
        flashTask = nil
        flash = nil
    }

    private func showFlash(_ message: String) {
        flashToken += 1
        let token = flashToken
        flash = RouterFlash(message: message)
        flashTask?.cancel()
        flashTask = Task { [weak self] in
            do {
                try await Task.sleep(for: .milliseconds(800))
            } catch {
                return
            }
            guard let self, token == self.flashToken else { return }
            self.flash = nil
        }
    }
}

extension WindowKeyboardRouter {
    /// The one construction path for a window's router: keymap, command
    /// context, palette suspension, and sidebar jumps wired together
    /// (called once per window by the scene root).
    static func forWindow(
        appModel: AppModel,
        windowState: WindowState,
        configStore: ConfigurationStore
    ) -> WindowKeyboardRouter {
        let context = CommandContext(
            appModel: appModel,
            windowState: windowState,
            configStore: configStore
        )
        let router = WindowKeyboardRouter(
            keymap: configStore.configuration.keymap,
            context: context
        )
        router.isSuspended = { windowState.commandPalettePresentation != nil }
        router.performJump = { jump in
            SidebarShortcuts.perform(jump, appModel: appModel, windowState: windowState)
        }
        return router
    }
}

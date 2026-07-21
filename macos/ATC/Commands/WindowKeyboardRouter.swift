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

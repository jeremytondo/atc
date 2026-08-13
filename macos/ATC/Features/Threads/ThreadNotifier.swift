// The one place the app decides a thread's change is worth interrupting the
// user for.
//
// Invariants:
//
// - **App-level and single.** `AppModel` owns exactly one notifier and drives
//   it from its own navigation observation. Windows never notify: two open
//   windows must produce one banner, not two.
// - **A banner fires on a transition but lives on a claim.** Only the two
//   transitions in `trigger(from:to:)` interrupt the user, and a delivered
//   banner survives only while what it claims is still true — so answering a
//   prompt in the TUI or viewing the thread from another client takes this
//   app's banner down without the user touching it. Notification Center must
//   never show a state the thread is no longer in.
// - **A suppressed banner is spent, never deferred.** Anything that happened
//   while ATC was frontmost is not replayed when the user switches away; the
//   sidebar's "Done" is the durable record.
// - **A connection's first resolved load seeds; it does not notify.** Diffing
//   is the default and silence is the one exception, which covers launch, a
//   newly added connection, and a rebuilt one. A reconnect needs no rule at
//   all: its baseline was never cleared, so finishes during a sleep or wifi
//   gap diff as new and notify.

import AppKit
import ATCAppServerAPI
import Foundation
import UserNotifications

final class ThreadNotifier {
    /// The UserDefaults key behind the Notifications settings switch. Off on a
    /// fresh install, so nothing asks for authorization at first launch.
    static let preferenceKey = "threadNotificationsEnabled"

    /// What a banner asserts about a thread right now. `needsInput` outranks
    /// `finished`: a turn that ended in a question is one interruption, and
    /// the question is the actionable half.
    enum Claim: Equatable {
        case finished
        case needsInput

        /// A banner is a poke, not a report: no agent name, no server name,
        /// and no message text — the API exposes no turn content anyway.
        func body(project: String?) -> String {
            let lead = switch self {
            case .finished: "Finished"
            case .needsInput: "Needs your input"
            }
            guard let project else { return lead }
            return "\(lead) · \(project)"
        }
    }

    /// One connection's threads as the notifier sees them. Archived threads
    /// are deliberately absent: they are never a live claim, and one leaving
    /// the active list reads here as a thread that is gone.
    struct ConnectionInput {
        let connectionID: UUID
        /// `ThreadsStore.isResolved`: false while the list is unloaded or its
        /// last refresh failed. Connection loss must not read as "nothing is
        /// happening" and take every banner down.
        let isResolved: Bool
        let projects: [Project]
        let threads: [ATCThread]
    }

    /// Set by the model: a banner click selects its thread.
    var onOpenThread: ((ThreadRef) -> Void)?

    /// The delivery seam. No-ops by default so tests and previews can never
    /// reach the developer's real Notification Center; `system()` is the one
    /// path that wires UserNotifications.
    var deliver: (_ id: String, _ title: String, _ body: String) -> Void = { _, _, _ in }
    var withdraw: (_ id: String) -> Void = { _ in }

    var isEnabled: () -> Bool = {
        UserDefaults.standard.bool(forKey: ThreadNotifier.preferenceKey)
    }

    /// Frontmost ATC never notifies — not "not this thread", nothing at all.
    /// Mirrors the seam at `WindowState.isAppActive`.
    var isAppActive: () -> Bool = { NSApplication.shared.isActive }

    /// The `(unread, activityState)` pair each thread showed on the last pass:
    /// the diff baseline. Both halves are kept because the triggers are
    /// transitions, not states — see `trigger(from:to:)`.
    private struct State: Equatable {
        let unread: Bool
        let activityState: ThreadActivityState
    }

    private var states: [ThreadRef: State] = [:]
    /// The banner each thread currently has on screen. Nothing was delivered
    /// while the preference was off or ATC was frontmost, so nothing is
    /// withdrawn for those; `system()` starts from an empty Notification
    /// Center, which is what makes this in-memory record authoritative.
    private var delivered: [ThreadRef: Claim] = [:]
    private var seeded: Set<UUID> = []
    /// `UNUserNotificationCenter.delegate` is a weak reference; nothing else
    /// would keep the click handler alive.
    private var clickHandler: NotificationClickHandler?

    /// The production notifier. Built from `ATCApp`'s stored state because its
    /// click delegate has to exist before the app finishes launching.
    static func system() -> ThreadNotifier {
        let notifier = ThreadNotifier()
        notifier.deliver = { id, title, body in
            let content = UNMutableNotificationContent()
            content.title = title
            content.body = body
            // Muting is System Settings' job, not a second ATC control.
            content.sound = .default
            // A nil trigger delivers immediately, and reusing the identifier
            // replaces the thread's previous banner instead of stacking one.
            UNUserNotificationCenter.current().add(
                UNNotificationRequest(identifier: id, content: content, trigger: nil)
            )
        }
        notifier.withdraw = { id in
            UNUserNotificationCenter.current().removeDeliveredNotifications(withIdentifiers: [id])
        }
        let handler = NotificationClickHandler { [weak notifier] ref in
            NSApplication.shared.activate()
            notifier?.onOpenThread?(ref)
        }
        notifier.clickHandler = handler
        UNUserNotificationCenter.current().delegate = handler
        // Banners this process cannot reason about must not linger: the
        // baseline starts empty, so anything left from a previous launch could
        // never be withdrawn once its claim stopped being true. Ordered after
        // the delegate so a click that launched the app is still delivered.
        UNUserNotificationCenter.current().removeAllDeliveredNotifications()
        return notifier
    }

    /// Diffs every resolved connection against the last pass. Driven by the
    /// model's navigation observation, which already fires on exactly the
    /// store changes read here.
    func reconcile(connections: [ConnectionInput]) {
        for connection in connections where connection.isResolved {
            apply(connection)
        }
    }

    /// Drops a connection's baseline and banners. Called when its runtime is
    /// torn down — removal and a URL/token rebuild alike — so the next load
    /// seeds silently instead of diffing against a server that is gone.
    func forget(connectionID: UUID) {
        for ref in Array(states.keys) where ref.connectionID == connectionID {
            states[ref] = nil
            takeDown(ref)
        }
        seeded.remove(connectionID)
    }

    // MARK: - Private

    private func apply(_ connection: ConnectionInput) {
        let isSeeding = !seeded.contains(connection.connectionID)
        seeded.insert(connection.connectionID)

        var present: Set<ThreadRef> = []
        for thread in connection.threads {
            let ref = ThreadRef(connectionID: connection.connectionID, threadID: thread.id)
            present.insert(ref)
            let state = State(unread: thread.unread, activityState: thread.activityState)
            let previous = states[ref]
            states[ref] = state
            // No baseline yet means this connection (or this thread) is new:
            // record it and stay quiet.
            guard !isSeeding, let previous, previous != state else { continue }
            if let claim = Self.trigger(from: previous, to: state) {
                post(claim, for: thread, ref: ref, in: connection)
                continue
            }
            takeDownIfStale(ref, state: state)
        }

        // A thread that left the active list — archived, deleted, or moved by
        // another client — cannot still be in the state its banner claims.
        for ref in Array(states.keys)
        where ref.connectionID == connection.connectionID && !present.contains(ref) {
            states[ref] = nil
            takeDown(ref)
        }
    }

    /// The two triggers, stated as transitions rather than states: a thread
    /// that was already unread when it stopped needing input has not finished
    /// anything new, and must not alert a second time.
    private static func trigger(from previous: State, to state: State) -> Claim? {
        if previous.activityState != .needsInput, state.activityState == .needsInput {
            return .needsInput
        }
        // `unknown` activity never triggers on its own: the server cannot tell
        // a crashed provider from a slow one. The finish it reports can.
        if !previous.unread, state.unread { return .finished }
        return nil
    }

    private func post(_ claim: Claim, for thread: ATCThread, ref: ThreadRef, in connection: ConnectionInput) {
        guard isEnabled(), !isAppActive() else { return }
        let project = connection.projects.first { $0.id == thread.projectId }?.name
        deliver(ref.notificationIdentifier, thread.displayName, claim.body(project: project))
        delivered[ref] = claim
    }

    private func takeDownIfStale(_ ref: ThreadRef, state: State) {
        guard let claim = delivered[ref] else { return }
        let stillTrue = switch claim {
        case .finished: state.unread
        case .needsInput: state.activityState == .needsInput
        }
        guard !stillTrue else { return }
        takeDown(ref)
    }

    private func takeDown(_ ref: ThreadRef) {
        guard delivered.removeValue(forKey: ref) != nil else { return }
        withdraw(ref.notificationIdentifier)
    }
}

/// Routes a banner click back into the app; separate only because the
/// UserNotifications delegate has to be an NSObject.
private final class NotificationClickHandler: NSObject, UNUserNotificationCenterDelegate {
    private let onClick: (ThreadRef) -> Void

    init(onClick: @escaping (ThreadRef) -> Void) {
        self.onClick = onClick
        super.init()
    }

    /// Nonisolated because the delegate is called off the main actor; only the
    /// identifier crosses back, so no notification object has to be Sendable.
    nonisolated func userNotificationCenter(
        _: UNUserNotificationCenter,
        didReceive response: UNNotificationResponse
    ) async {
        let identifier = response.notification.request.identifier
        await MainActor.run {
            guard let ref = ThreadRef(notificationIdentifier: identifier) else { return }
            onClick(ref)
        }
    }
}

extension ThreadNotifier.ConnectionInput {
    init(runtime: ConnectionRuntime) {
        self.init(
            connectionID: runtime.id,
            isResolved: runtime.threads.isResolved,
            projects: runtime.projects.projects,
            threads: runtime.threads.threads
        )
    }
}

extension ThreadRef {
    /// Stable per thread, so a new claim replaces that thread's banner instead
    /// of stacking a second one. Thread IDs are UUIDv7 and connection IDs are
    /// UUIDs, so neither can contain the separator.
    var notificationIdentifier: String {
        "thread/\(connectionID.uuidString)/\(threadID)"
    }

    init?(notificationIdentifier: String) {
        let parts = notificationIdentifier.split(separator: "/")
        guard parts.count == 3, parts[0] == "thread",
              let connectionID = UUID(uuidString: String(parts[1]))
        else { return nil }
        self.init(connectionID: connectionID, threadID: String(parts[2]))
    }
}

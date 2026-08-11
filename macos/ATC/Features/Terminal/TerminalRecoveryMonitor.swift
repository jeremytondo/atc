import AppKit
import Foundation
import Network

/// One app-wide source of recovery signals for retained terminal attaches.
/// An initial healthy path is normal startup, not recovery; only a transition
/// from an unavailable path to `.satisfied` emits.
final class TerminalRecoveryMonitor {
    var onRecovery: (() -> Void)?

    private let notificationCenter: NotificationCenter?
    private let wakeNotification: Notification.Name
    private let pathMonitor: NWPathMonitor?
    private var wakeTask: Task<Void, Never>?
    private var pathTask: Task<Void, Never>?
    private var previousPathWasSatisfied: Bool?
    private var started = false

    init(
        notificationCenter: NotificationCenter? = NSWorkspace.shared.notificationCenter,
        wakeNotification: Notification.Name = NSWorkspace.didWakeNotification,
        pathMonitor: NWPathMonitor? = NWPathMonitor()
    ) {
        self.notificationCenter = notificationCenter
        self.wakeNotification = wakeNotification
        self.pathMonitor = pathMonitor
    }

    static func disabled() -> TerminalRecoveryMonitor {
        TerminalRecoveryMonitor(notificationCenter: nil, pathMonitor: nil)
    }

    func start() {
        guard !started else { return }
        started = true

        if let notificationCenter {
            let notifications = notificationCenter.notifications(named: wakeNotification)
            wakeTask = Task { [weak self] in
                for await _ in notifications {
                    guard let self else { return }
                    self.onRecovery?()
                }
            }
        }

        // Iterating the monitor starts it; `stop()` cancels both the task and
        // the monitor so the sequence ends.
        if let pathMonitor {
            pathTask = Task { [weak self] in
                for await path in pathMonitor {
                    guard let self else { return }
                    self.recordNetworkPath(isSatisfied: path.status == .satisfied)
                }
            }
        }
    }

    func stop() {
        guard started else { return }
        started = false
        wakeTask?.cancel()
        wakeTask = nil
        pathTask?.cancel()
        pathTask = nil
        pathMonitor?.cancel()
    }

    /// Internal so transition behavior can be tested without depending on
    /// the machine's live network configuration.
    func recordNetworkPath(isSatisfied: Bool) {
        defer { previousPathWasSatisfied = isSatisfied }
        guard previousPathWasSatisfied == false, isSatisfied else { return }
        onRecovery?()
    }
}

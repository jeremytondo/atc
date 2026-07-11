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
    private let pathQueue = DispatchQueue(label: "ElevenIdeas.atc.terminal-path")
    private var wakeObserver: (any NSObjectProtocol)?
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
            wakeObserver = notificationCenter.addObserver(
                forName: wakeNotification,
                object: nil,
                queue: .main
            ) { [weak self] _ in
                Task { @MainActor [weak self] in
                    self?.onRecovery?()
                }
            }
        }

        if let pathMonitor {
            pathMonitor.pathUpdateHandler = { [weak self] path in
                let isSatisfied = path.status == .satisfied
                Task { @MainActor [weak self] in
                    self?.recordNetworkPath(isSatisfied: isSatisfied)
                }
            }
            pathMonitor.start(queue: pathQueue)
        }
    }

    func stop() {
        guard started else { return }
        started = false
        pathMonitor?.pathUpdateHandler = nil
        pathMonitor?.cancel()
        if let wakeObserver, let notificationCenter {
            notificationCenter.removeObserver(wakeObserver)
        }
        wakeObserver = nil
    }

    /// Internal so transition behavior can be tested without depending on
    /// the machine's live network configuration.
    func recordNetworkPath(isSatisfied: Bool) {
        defer { previousPathWasSatisfied = isSatisfied }
        guard previousPathWasSatisfied == false, isSatisfied else { return }
        onRecovery?()
    }

    deinit {
        pathMonitor?.pathUpdateHandler = nil
        pathMonitor?.cancel()
        if let wakeObserver, let notificationCenter {
            notificationCenter.removeObserver(wakeObserver)
        }
    }
}

import Foundation
import Testing
import ATCAPI
@testable import ATC

/// Registry membership in `AppModel.terminals` means ownership (scrollback
/// survives sidebar navigation); liveness is the controller's phase. These
/// tests pin the distinction so ended terminals stop reading as "Connected".
@Suite("Terminal liveness")
struct TerminalLivenessTests {
    @Test("every ended phase reads as not actively attached")
    func endedPhases() {
        let ended: [AttachEndReason] = [
            .sessionEnded,
            .serverError,
            .transportFailure("socket closed"),
            .closedByClient,
        ]
        for reason in ended {
            #expect(!TerminalSessionController.Phase.ended(reason).isActivelyAttached)
        }
        #expect(TerminalSessionController.Phase.connecting.isActivelyAttached)
        #expect(TerminalSessionController.Phase.connected.isActivelyAttached)
    }

    @MainActor
    @Test("an ended controller is retained for history but not live")
    func endedControllerRetainedNotLive() async throws {
        let appModel = AppModel.preview()
        let runtime = try #require(appModel.runtimes.first)
        for _ in 0..<100 where runtime.sessions.sessions.isEmpty {
            try await Task.sleep(for: .milliseconds(20))
        }
        let session = try #require(runtime.sessions.sessions.first { $0.status == .live })
        let ref = SessionRef(connectionID: runtime.id, sessionID: session.id)

        appModel.attachIfNeeded(to: session, connectionID: runtime.id)
        let controller = try #require(appModel.terminals[ref])
        // Freshly attached: connecting counts as live for indicators.
        #expect(appModel.hasLiveTerminals(connectionID: runtime.id))
        #expect(appModel.activelyAttachedRefs.contains(ref))

        // Ending the attach must not remove the controller (scrollback is
        // retained) but must remove it from every liveness affordance.
        controller.disconnect()
        #expect(appModel.terminals[ref] != nil)
        #expect(controller.phase == .ended(.closedByClient))
        #expect(!appModel.hasLiveTerminals(connectionID: runtime.id))
        #expect(!appModel.activelyAttachedRefs.contains(ref))
    }

    @MainActor
    @Test("a socket-reported session end reconciles the store and removes the terminal")
    func socketEndReconcilesStoreAndRemovesTerminal() async throws {
        let appModel = AppModel.preview()
        let runtime = try #require(appModel.runtimes.first)
        for _ in 0..<100 where runtime.sessions.sessions.isEmpty {
            try await Task.sleep(for: .milliseconds(20))
        }
        let session = try #require(runtime.sessions.sessions.first { $0.status == .live })
        let ref = SessionRef(connectionID: runtime.id, sessionID: session.id)

        appModel.attachIfNeeded(to: session, connectionID: runtime.id)
        let controller = try #require(appModel.terminals[ref])

        // The controller reporting an authoritative end (409 stale attach or
        // a normal WebSocket closure) must flip the stored session to Ended
        // and tear the terminal down so input is impossible.
        controller.onSessionEnded?()
        let reconciled = try #require(runtime.sessions.sessions.first { $0.id == session.id })
        #expect(reconciled.status == .ended)
        #expect(appModel.terminals[ref] == nil)
    }

    @MainActor
    @Test("explicit disconnect removes the controller from the registry")
    func explicitDisconnectRemoves() async throws {
        let appModel = AppModel.preview()
        let runtime = try #require(appModel.runtimes.first)
        for _ in 0..<100 where runtime.sessions.sessions.isEmpty {
            try await Task.sleep(for: .milliseconds(20))
        }
        let session = try #require(runtime.sessions.sessions.first { $0.status == .live })
        let ref = SessionRef(connectionID: runtime.id, sessionID: session.id)

        appModel.attachIfNeeded(to: session, connectionID: runtime.id)
        appModel.disconnectTerminal(ref: ref)
        #expect(appModel.terminals[ref] == nil)
        #expect(!appModel.hasLiveTerminals(connectionID: runtime.id))
    }
}

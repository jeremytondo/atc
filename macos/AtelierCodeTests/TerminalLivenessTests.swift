import Foundation
import Testing
import AtelierCodeAPI
@testable import AtelierCode

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
        let session = try #require(runtime.sessions.sessions.first { $0.attachable })
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
    @Test("explicit disconnect removes the controller from the registry")
    func explicitDisconnectRemoves() async throws {
        let appModel = AppModel.preview()
        let runtime = try #require(appModel.runtimes.first)
        for _ in 0..<100 where runtime.sessions.sessions.isEmpty {
            try await Task.sleep(for: .milliseconds(20))
        }
        let session = try #require(runtime.sessions.sessions.first { $0.attachable })
        let ref = SessionRef(connectionID: runtime.id, sessionID: session.id)

        appModel.attachIfNeeded(to: session, connectionID: runtime.id)
        appModel.disconnectTerminal(ref: ref)
        #expect(appModel.terminals[ref] == nil)
        #expect(!appModel.hasLiveTerminals(connectionID: runtime.id))
    }
}

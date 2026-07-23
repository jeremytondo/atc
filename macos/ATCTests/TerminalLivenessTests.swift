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
    @Test("only a session_ended close reconciles the store and removes the terminal")
    func socketEndRequiresAuthoritativeReason() async throws {
        let appModel = AppModel.preview()
        let runtime = try #require(appModel.runtimes.first)
        for _ in 0..<100 where runtime.sessions.sessions.isEmpty {
            try await Task.sleep(for: .milliseconds(20))
        }
        let session = try #require(runtime.sessions.sessions.first { $0.status == .live })
        let ref = SessionRef(connectionID: runtime.id, sessionID: session.id)

        appModel.attachIfNeeded(to: session, connectionID: runtime.id)
        let controller = try #require(appModel.terminals[ref])

        let plainClose = AttachConnection.endReason(
            closeCode: .normalClosure,
            closeReason: nil,
            errorDescription: "closed"
        )
        #expect(plainClose == .transportFailure("closed"))
        #expect(runtime.sessions.sessions.first { $0.id == session.id }?.status == .live)
        #expect(appModel.terminals[ref] != nil)

        let confirmedClose = AttachConnection.endReason(
            closeCode: .normalClosure,
            closeReason: Data("session_ended".utf8),
            errorDescription: "closed"
        )
        #expect(confirmedClose == .sessionEnded)
        if confirmedClose == .sessionEnded {
            controller.onSessionEnded?()
        }
        let reconciled = try #require(runtime.sessions.sessions.first { $0.id == session.id })
        #expect(reconciled.status == .ended)
        #expect(appModel.terminals[ref] == nil)
    }

    @Test("retryable close reasons never classify as a session end")
    func retryableCloseReasonsStayLive() {
        for reason in ["attach_failed", "zmx_unavailable"] {
            #expect(AttachConnection.endReason(
                closeCode: .internalServerError,
                closeReason: Data(reason.utf8),
                errorDescription: reason
            ) == .serverError)
        }
        #expect(AttachConnection.endReason(
            closeCode: .normalClosure,
            closeReason: Data("other".utf8),
            errorDescription: "other"
        ) == .transportFailure("other"))
        #expect(AttachConnection.endReason(
            statusCode: 409,
            closeCode: .normalClosure,
            closeReason: nil,
            errorDescription: "handshake"
        ) == .sessionEnded)
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

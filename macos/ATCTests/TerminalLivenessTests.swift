import Foundation
import Testing
import ATCAppServerTransport
@testable import ATC

/// Registry membership in `AppModel.terminals` means ownership (scrollback
/// survives sidebar navigation); liveness is the controller's phase. These
/// tests pin the distinction so an ended terminal never reads as "Connected",
/// and a retained one is never mistaken for a live attach.
@MainActor
@Suite("Terminal liveness")
struct TerminalLivenessTests {
    @Test("only the ended phase reads as not actively attached")
    func phaseTruthTable() {
        let ended: [AttachEndReason] = [
            .terminalEnded,
            .rejected(statusCode: 404),
            .retryable("socket closed"),
            .closedByClient,
        ]
        for reason in ended {
            #expect(!TerminalSessionController.Phase.ended(reason).isActivelyAttached)
        }
        #expect(TerminalSessionController.Phase.connecting.isActivelyAttached)
        #expect(TerminalSessionController.Phase.connected.isActivelyAttached)
        // Reconnecting counts as attached so the banner never flickers back
        // through "Connecting" on every retry cycle.
        #expect(TerminalSessionController.Phase.reconnecting.isActivelyAttached)
    }

    @Test("a terminal's server status drives whether it can be attached at all")
    func serverStatusDrivesAttach() {
        #expect(Fixtures.terminal(id: "t", status: .live).isLive)
        #expect(!Fixtures.terminal(id: "t", status: .ended).isLive)
    }

    @Test("an ended controller stays in the registry for history but not for liveness")
    func endedControllerRetainedNotLive() async throws {
        let client = ScriptableAppServerClient()
        Fixtures.seed(client)
        let test = try await makeModel(client: client)
        await test.runtime.refresh()

        let terminal = try #require(test.runtime.terminals.terminal(id: "trm_live"))
        test.model.attachIfNeeded(to: terminal, connectionID: test.connectionID)
        let ref = test.terminalRef("trm_live")
        let controller = try #require(test.model.terminals[ref])
        #expect(test.model.hasLiveTerminals(connectionID: test.connectionID))

        test.attach.send(.ended(.terminalEnded), attempt: 0, finish: true)
        await settle(until: { controller.phase == .ended(.terminalEnded) })

        // The surface is kept for its final frame; every liveness affordance
        // drops it.
        #expect(test.model.terminals[ref] != nil)
        #expect(!controller.isActivelyAttached)
        #expect(!test.model.hasLiveTerminals(connectionID: test.connectionID))
        #expect(!test.model.activelyAttachedRefs.contains(ref))
    }

    @Test("a terminal ending mid-attach refetches so the tombstone lands")
    func endedTerminalRefetchesStores() async throws {
        let client = ScriptableAppServerClient()
        Fixtures.seed(client)
        let test = try await makeModel(client: client)
        await test.runtime.refresh()

        let terminal = try #require(test.runtime.terminals.terminal(id: "trm_live"))
        test.model.attachIfNeeded(to: terminal, connectionID: test.connectionID)

        client.terminals = client.terminals.map { existing in
            guard existing.id == "trm_live" else { return existing }
            var ended = existing
            ended.status = .ended
            return ended
        }
        test.attach.send(.ended(.terminalEnded), attempt: 0, finish: true)

        await settle(until: {
            test.runtime.terminals.terminal(id: "trm_live")?.isLive == false
        })
        #expect(test.runtime.terminals.terminal(id: "trm_live")?.isLive == false)
    }

    @Test("an explicit disconnect removes the controller from the registry")
    func explicitDisconnectRemoves() async throws {
        let client = ScriptableAppServerClient()
        Fixtures.seed(client)
        let test = try await makeModel(client: client)
        await test.runtime.refresh()

        let terminal = try #require(test.runtime.terminals.terminal(id: "trm_live"))
        test.model.attachIfNeeded(to: terminal, connectionID: test.connectionID)
        let ref = test.terminalRef("trm_live")

        test.model.disconnectTerminal(ref: ref)
        #expect(test.model.terminals[ref] == nil)
        #expect(!test.model.hasLiveTerminals(connectionID: test.connectionID))
    }
}

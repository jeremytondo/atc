import ATCAppServerAPI
import Foundation
import Testing

@testable import ATCChat

/// The builder's request placement: a pending request lands on the row of
/// the item it blocks (any depth) and is reported as attached; requests
/// without an item — or whose item is off-page — stay for the bar.
@MainActor
@Suite("Chat row builder requests")
struct ChatRowRequestTests {
    private func command(_ id: String, parent: String? = nil) -> ThreadItem {
        .command(
            .init(
                _type: .command, id: id, turnId: "t1", parentItemId: parent, title: "bun test",
                status: .running, command: "bun test"))
    }

    private func approval(_ id: String, itemId: String?) -> ThreadRequest {
        .approval(
            .init(
                kind: .approval, id: id, turnId: "t1", itemId: itemId, openedAt: Date(timeIntervalSince1970: 0),
                title: "Run bun test?", subject: .command(.init(_type: .command, command: "bun test"))))
    }

    private func built(requests: [ThreadRequest]) -> (rows: [ChatRowModel], attached: Set<String>) {
        var transcript = ChatTranscript()
        transcript.load(
            ThreadTranscriptPage(
                items: [command("c1"), command("c2", parent: "c1")],
                turns: [ThreadTurn(id: "t1", status: .running)],
                seq: 1, snapshotVersion: 1, hasMore: false
            ))
        transcript.replaceRequests(requests)
        var boxes: [String: ChatItemModel] = [:]
        var folds: [String: ChatWorkModel] = [:]
        let result = ChatRowBuilder.rows(from: transcript, reusing: &boxes, folds: &folds)
        return (result.rows, result.attachedRequestIDs)
    }

    @Test("a request attaches to the row of the item it blocks, nested included")
    func attachesToItem() {
        let (rows, attached) = built(requests: [approval("r1", itemId: "c2")])
        #expect(attached == ["r1"])
        guard case .work(let work) = rows.first else {
            Issue.record("expected the fold")
            return
        }
        #expect(work.steps.first?.children.first?.request?.id == "r1")
    }

    @Test("a request without an item, or for an off-page item, stays unattached")
    func staysInBar() {
        let (_, attachedNone) = built(requests: [approval("r1", itemId: nil)])
        #expect(attachedNone.isEmpty)
        let (_, attachedOffPage) = built(requests: [approval("r2", itemId: "older-item")])
        #expect(attachedOffPage.isEmpty)
    }
}

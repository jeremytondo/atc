import Testing
import ATCAPI
@testable import ATC

@Suite("SessionKind")
struct SessionKindTests {
    private func session(
        name: String? = nil,
        actionId: String? = "act_x",
        actionName: String? = "Action",
        isAgent: Bool = false
    ) -> Session {
        Session(
            id: "ses_x",
            name: name,
            actionId: actionId,
            actionName: actionName,
            isAgent: isAgent,
            workingDir: "/home/dev",
            status: .live,
            createdAt: .now,
            updatedAt: .now
        )
    }

    @Test("Interactive Shell is always a Terminal")
    func shellIsTerminal() {
        #expect(SessionKind.classify(
            session: session(actionId: nil, actionName: nil, isAgent: true)
        ) == .terminal)
    }

    @Test("copied isAgent classifies Action sessions")
    func copiedClassification() {
        #expect(SessionKind.classify(session: session(isAgent: true)) == .agent)
        #expect(SessionKind.classify(session: session(isAgent: false)) == .terminal)
    }

    @Test("classification does not need the current Action registry")
    func deletedActionKeepsClassification() {
        let deletedAgent = session(
            actionId: "act_deleted",
            actionName: "Deleted Agent",
            isAgent: true
        )

        #expect(SessionKind.classify(session: deletedAgent) == .agent)
        #expect(SessionKind.displayName(session: deletedAgent) == "Deleted Agent")
    }

    @Test("display name uses session name, copied action name, then Terminal")
    func displayNameFallbacks() {
        #expect(SessionKind.displayName(session: session(name: "Fix parser")) == "Fix parser")
        #expect(SessionKind.displayName(session: session(name: "")) == "")
        #expect(SessionKind.displayName(
            session: session(actionId: nil, actionName: nil)
        ) == "Terminal")
    }

    @Test("toolbar uses the same stable display fields")
    func toolbarLabel() {
        #expect(SessionKind.toolbarLabel(
            session: session(name: "Fix parser", actionName: "Claude", isAgent: true)
        ) == "Fix parser")
        #expect(SessionKind.toolbarLabel(
            session: session(name: nil, actionName: "Claude", isAgent: true)
        ) == "Claude")
    }
}

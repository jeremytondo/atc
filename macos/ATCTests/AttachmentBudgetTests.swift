import Foundation
import Testing
import ATCAPI
@testable import ATC

/// The LRU attachment budget: attaching past the budget evicts the
/// least-recently-used terminal through the standard disconnect path,
/// while the selected session and the open Workspace's sessions are
/// pinned.
@MainActor
@Suite("Attachment budget")
struct AttachmentBudgetTests {
    /// AppModel over an ephemeral store whose terminal controllers never
    /// open a real socket (their attach stream never yields).
    private func makeModel(budget: Int) -> AppModel {
        let suite = "AttachmentBudgetTests.\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        defaults.removePersistentDomain(forName: suite)
        let store = ConnectionsStore(defaults: defaults, credentials: InMemoryCredentialStore())
        return AppModel(
            connections: store,
            clientFactory: { _ in MockATCClient() },
            terminalControllerFactory: { sessionID, client in
                TerminalSessionController(
                    sessionID: sessionID,
                    client: client,
                    connectionFactory: { _, _ in
                        TerminalAttachHandle(
                            start: { AsyncStream { _ in } },
                            enqueue: { _ in },
                            enqueueResize: { _, _ in },
                            close: {}
                        )
                    }
                )
            },
            terminalRecoveryMonitor: .disabled(),
            attachmentBudget: budget
        )
    }

    private func session(_ id: String, workspace: String? = nil) -> Session {
        Session(
            id: id, environment: "host", workingDir: "/home/dev",
            status: .running, attachable: true,
            createdAt: .now, updatedAt: .now,
            workspace: workspace.map { SessionWorkspace(id: $0, name: $0) }
        )
    }

    @Test("attaching past the budget evicts the least-recently-used terminal")
    func evictsLRU() throws {
        let model = makeModel(budget: 2)
        let record = try model.addConnection(name: "A", urlString: "http://a:1", token: "")
        model.runtime(id: record.id)!.stopPolling()

        let refs = (1...3).map { SessionRef(connectionID: record.id, sessionID: "s\($0)") }
        for index in 1...3 {
            model.attachIfNeeded(to: session("s\(index)"), connectionID: record.id)
        }
        #expect(model.terminals.count == 2)
        #expect(model.terminals[refs[0]] == nil)
        #expect(model.terminals[refs[1]] != nil)
        #expect(model.terminals[refs[2]] != nil)
    }

    @Test("selecting a terminal refreshes its LRU position")
    func selectionTouchesLRU() throws {
        let model = makeModel(budget: 2)
        let record = try model.addConnection(name: "A", urlString: "http://a:1", token: "")
        model.runtime(id: record.id)!.stopPolling()

        model.attachIfNeeded(to: session("s1"), connectionID: record.id)
        model.attachIfNeeded(to: session("s2"), connectionID: record.id)
        // Re-selecting s1 makes s2 the LRU candidate...
        model.selection = SessionRef(connectionID: record.id, sessionID: "s1")
        // ...then s1 is deselected again so nothing is pinned.
        model.selection = nil
        model.attachIfNeeded(to: session("s3"), connectionID: record.id)
        #expect(model.terminals[SessionRef(connectionID: record.id, sessionID: "s1")] != nil)
        #expect(model.terminals[SessionRef(connectionID: record.id, sessionID: "s2")] == nil)
    }

    @Test("the selected session is never evicted")
    func selectionIsPinned() throws {
        let model = makeModel(budget: 2)
        let record = try model.addConnection(name: "A", urlString: "http://a:1", token: "")
        model.runtime(id: record.id)!.stopPolling()

        let pinned = SessionRef(connectionID: record.id, sessionID: "s1")
        model.attachIfNeeded(to: session("s1"), connectionID: record.id)
        model.selection = pinned
        model.attachIfNeeded(to: session("s2"), connectionID: record.id)
        model.attachIfNeeded(to: session("s3"), connectionID: record.id)
        #expect(model.terminals.count == 2)
        #expect(model.terminals[pinned] != nil)
        // s2 (the oldest unpinned attach) was the eviction victim.
        #expect(model.terminals[SessionRef(connectionID: record.id, sessionID: "s2")] == nil)
    }

    @Test("the open workspace's sessions are never evicted")
    func openWorkspaceIsPinned() async throws {
        let model = makeModel(budget: 1)
        let record = try model.addConnection(name: "A", urlString: "http://a:1", token: "")
        let runtime = model.runtime(id: record.id)!
        runtime.stopPolling()
        await runtime.refresh()

        // ses_running belongs to wsp_parser in the fixtures.
        model.openWorkspace = WorkspaceRef(connectionID: record.id, workspaceID: "wsp_parser")
        let pinned = SessionRef(connectionID: record.id, sessionID: "ses_running")
        let fixture = try #require(runtime.sessions.session(id: "ses_running"))
        model.attachIfNeeded(to: fixture, connectionID: record.id)

        model.attachIfNeeded(to: session("s_other"), connectionID: record.id)
        #expect(model.terminals[pinned] != nil)
        #expect(model.terminals[SessionRef(connectionID: record.id, sessionID: "s_other")] != nil)
    }

    @Test("pinned refs alone may exceed the budget until the workspace closes")
    func pinnedRefsExceedBudget() async throws {
        let model = makeModel(budget: 1)
        let record = try model.addConnection(name: "A", urlString: "http://a:1", token: "")
        let runtime = model.runtime(id: record.id)!
        runtime.stopPolling()
        await runtime.refresh()

        model.openWorkspace = WorkspaceRef(connectionID: record.id, workspaceID: "wsp_parser")
        // Both fixtures are wsp_parser members: both pinned, budget 1.
        let running = try #require(runtime.sessions.session(id: "ses_running"))
        let shell = try #require(runtime.sessions.session(id: "ses_shell"))
        model.attachIfNeeded(to: running, connectionID: record.id)
        model.attachIfNeeded(to: shell, connectionID: record.id)
        #expect(model.terminals.count == 2)

        // Closing the workspace unpins; the next attach evicts down.
        model.openWorkspace = nil
        model.attachIfNeeded(to: session("s_new"), connectionID: record.id)
        #expect(model.terminals.count == 1)
    }

    @Test("eviction goes through the standard disconnect path")
    func evictionDisconnects() throws {
        let model = makeModel(budget: 1)
        let record = try model.addConnection(name: "A", urlString: "http://a:1", token: "")
        model.runtime(id: record.id)!.stopPolling()

        model.attachIfNeeded(to: session("s1"), connectionID: record.id)
        let ref = SessionRef(connectionID: record.id, sessionID: "s1")
        let controller = try #require(model.terminals[ref])
        #expect(controller.isActivelyAttached)

        model.attachIfNeeded(to: session("s2"), connectionID: record.id)
        #expect(model.terminals[ref] == nil)
        // The evicted controller was disconnected, not leaked live.
        #expect(!controller.isActivelyAttached)
    }

    @Test("reselecting an evicted session reattaches through attachIfNeeded")
    func evictedSessionReattaches() throws {
        let model = makeModel(budget: 1)
        let record = try model.addConnection(name: "A", urlString: "http://a:1", token: "")
        model.runtime(id: record.id)!.stopPolling()

        model.attachIfNeeded(to: session("s1"), connectionID: record.id)
        model.attachIfNeeded(to: session("s2"), connectionID: record.id)
        let ref = SessionRef(connectionID: record.id, sessionID: "s1")
        #expect(model.terminals[ref] == nil)

        // The same path selection takes on a cold session.
        model.selection = ref
        model.attachIfNeeded(to: session("s1"), connectionID: record.id)
        #expect(model.terminals[ref] != nil)
        #expect(model.terminals.count == 1)
    }
}

import ATCAppServerAPI
import Foundation
import Testing

@testable import ATC

@Suite("Dashboard model")
struct DashboardModelTests {
    private let local = UUID()
    private let remote = UUID()

    private func input(
        _ connectionID: UUID,
        name: String,
        urlString: String = "http://127.0.0.1:7331",
        projects: [Project],
        threads: [ATCThread] = [],
        terminals: [Terminal] = []
    ) -> DashboardModel.ConnectionInput {
        DashboardModel.ConnectionInput(
            connectionID: connectionID,
            connectionName: name,
            urlString: urlString,
            projects: projects,
            threads: threads,
            terminals: terminals
        )
    }

    @Test("a card counts its own active threads, standalone terminals, and waiting threads")
    func cardCounts() throws {
        let model = DashboardModel(inputs: [
            input(
                local,
                name: "Local",
                projects: [
                    Fixtures.project(id: "p1", name: "App"),
                    Fixtures.project(id: "p2", name: "Website"),
                ],
                threads: [
                    Fixtures.thread(id: "t1", projectId: "p1"),
                    Fixtures.thread(id: "t2", projectId: "p1", activityState: .needsInput),
                    // Archived threads are not active work.
                    Fixtures.thread(id: "t3", projectId: "p1", archivedAt: Fixtures.date(1)),
                    Fixtures.thread(id: "t4", projectId: "p2"),
                ],
                terminals: [
                    Fixtures.terminal(id: "standalone", projectId: "p1"),
                    // A thread's TUI terminal belongs to the thread.
                    Fixtures.terminal(id: "tui", projectId: "p1", threadId: "t1"),
                ]
            )
        ])
        let cards = try #require(model.sections.first?.cards)
        #expect(cards.map(\.project.id) == ["p1", "p2"])
        #expect(cards[0].activeThreadCount == 2)
        #expect(cards[0].standaloneTerminalCount == 1)
        #expect(cards[0].needsInputCount == 1)
        #expect(cards[1].activeThreadCount == 1)
        #expect(cards[1].standaloneTerminalCount == 0)
        #expect(cards[1].needsInputCount == 0)
        #expect(model.totalProjectCount == 2)
    }

    @Test("each connection is its own section, labeled by where it runs")
    func sectionsPerConnection() {
        let model = DashboardModel(inputs: [
            input(local, name: "Local", projects: [Fixtures.project(id: "p1", name: "App")]),
            input(
                remote,
                name: "Production",
                urlString: "https://build.example.com:7331",
                projects: []
            ),
        ])
        #expect(model.sections.map(\.connectionID) == [local, remote])
        #expect(model.sections.map(\.contextLabel) == ["Local", "build.example.com"])
        #expect(model.sections.map { $0.cards.count } == [1, 0])
        #expect(model.totalProjectCount == 1)
    }
}

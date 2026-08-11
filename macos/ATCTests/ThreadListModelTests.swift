import Foundation
import Testing
@testable import ATC

@Suite("Thread list model")
struct ThreadListModelTests {
    private let local = UUID()
    private let remote = UUID()

    private func input(
        _ connectionID: UUID,
        name: String,
        projects: [Project] = [Fixtures.project(id: "prj", name: "App")],
        threads: [ATCThread] = [],
        archived: [ATCThread] = []
    ) -> ThreadListModel.ConnectionInput {
        ThreadListModel.ConnectionInput(
            connectionID: connectionID,
            connectionName: name,
            projects: projects,
            threads: threads,
            archivedThreads: archived
        )
    }

    // MARK: - Labels

    @Test("a project name shared across connections carries its connection")
    func duplicateProjectNames() {
        let model = ThreadListModel(
            inputs: [
                input(local, name: "Local", projects: [
                    Fixtures.project(id: "p1", name: "App"),
                    Fixtures.project(id: "p2", name: "Website"),
                ]),
                input(remote, name: "Production", projects: [
                    Fixtures.project(id: "p3", name: "App"),
                ]),
            ],
            filter: .all
        )
        #expect(model.projects.map(\.label) == ["App · Local", "Website", "App · Production"])
    }

    @Test("a thread's label follows its project, and an unknown project reads as such")
    func threadLabels() {
        let model = ThreadListModel(
            inputs: [
                input(
                    local,
                    name: "Local",
                    threads: [
                        Fixtures.thread(id: "t1", projectId: "prj"),
                        Fixtures.thread(id: "t2", projectId: "gone"),
                    ]
                ),
            ],
            filter: .all
        )
        #expect(model.recent.map(\.projectLabel) == ["App", "Unknown Project"])
        #expect(model.recent.allSatisfy { $0.connectionName == "Local" })
    }

    // MARK: - Working directory

    @Test("a working directory shows only when it differs from the project default")
    func distinctWorkingDirectory() {
        let model = ThreadListModel(
            inputs: [
                input(
                    local,
                    name: "Local",
                    projects: [Fixtures.project(id: "prj", name: "App", workingDirectory: "/src/app")],
                    threads: [
                        Fixtures.thread(id: "same", workingDirectory: "/src/app", createdAt: Fixtures.date(10)),
                        Fixtures.thread(id: "other", workingDirectory: "/src/app/pkg", createdAt: Fixtures.date(20)),
                    ]
                ),
            ],
            filter: .all
        )
        let byID = Dictionary(uniqueKeysWithValues: model.recent.map { ($0.ref.threadID, $0) })
        #expect(byID["same"]?.distinctWorkingDirectory == nil)
        #expect(byID["other"]?.distinctWorkingDirectory == "/src/app/pkg")
    }

    // MARK: - Ordering

    @Test("pins are oldest-pin-first, recents newest-created-first, archived newest-archived-first")
    func ordering() {
        let model = ThreadListModel(
            inputs: [
                input(
                    local,
                    name: "Local",
                    threads: [
                        Fixtures.thread(id: "pin_new", pinnedAt: Fixtures.date(200), createdAt: Fixtures.date(1)),
                        Fixtures.thread(id: "pin_old", pinnedAt: Fixtures.date(100), createdAt: Fixtures.date(2)),
                        Fixtures.thread(id: "old", createdAt: Fixtures.date(10)),
                        Fixtures.thread(id: "new", createdAt: Fixtures.date(20)),
                    ],
                    archived: [
                        Fixtures.thread(id: "arch_old", archivedAt: Fixtures.date(300)),
                        Fixtures.thread(id: "arch_new", archivedAt: Fixtures.date(400)),
                    ]
                ),
            ],
            filter: .all
        )
        #expect(model.pinned.map(\.ref.threadID) == ["pin_old", "pin_new"])
        #expect(model.recent.map(\.ref.threadID) == ["new", "old"])
        #expect(model.archived.map(\.ref.threadID) == ["arch_new", "arch_old"])
    }

    // MARK: - Filtering

    @Test("the filter governs the thread list only; pins and archived stay global")
    func filterScope() {
        let inputs = [
            input(
                local,
                name: "Local",
                projects: [
                    Fixtures.project(id: "p1", name: "App"),
                    Fixtures.project(id: "p2", name: "Website"),
                ],
                threads: [
                    Fixtures.thread(id: "app", projectId: "p1"),
                    Fixtures.thread(id: "web", projectId: "p2"),
                    Fixtures.thread(id: "pinned", projectId: "p2", pinnedAt: Fixtures.date(1)),
                ],
                archived: [Fixtures.thread(id: "arch", projectId: "p2", archivedAt: Fixtures.date(2))]
            ),
        ]
        let filtered = ThreadListModel(
            inputs: inputs,
            filter: .project(ProjectRef(connectionID: local, projectID: "p1"))
        )
        #expect(filtered.recent.map(\.ref.threadID) == ["app"])
        #expect(filtered.pinned.map(\.ref.threadID) == ["pinned"])
        #expect(filtered.archived.map(\.ref.threadID) == ["arch"])

        // Same project ID on another Connection is a different project.
        let otherConnection = ThreadListModel(
            inputs: inputs,
            filter: .project(ProjectRef(connectionID: remote, projectID: "p1"))
        )
        #expect(otherConnection.recent.isEmpty)

        let archivedFilter = ThreadListModel(inputs: inputs, filter: .archived)
        #expect(archivedFilter.recent.isEmpty)
        #expect(archivedFilter.archived.map(\.ref.threadID) == ["arch"])
    }

    // MARK: - Row identity

    @Test("row identity is section-scoped, so pin and archive are not a move")
    func sectionScopedIdentity() {
        let ref = ThreadRef(connectionID: local, threadID: "t1")
        let plain = ThreadListItem.ID(ref: ref, isPinned: false, isArchived: false)
        let pinned = ThreadListItem.ID(ref: ref, isPinned: true, isArchived: false)
        let archived = ThreadListItem.ID(ref: ref, isPinned: false, isArchived: true)
        #expect(plain != pinned)
        #expect(plain != archived)

        let model = ThreadListModel(
            inputs: [
                input(
                    local,
                    name: "Local",
                    threads: [Fixtures.thread(id: "t1", pinnedAt: Fixtures.date(1))]
                ),
            ],
            filter: .all
        )
        #expect(model.pinned.map(\.id) == [pinned])
    }
}

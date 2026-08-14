import Foundation
import Testing

@testable import ATC

@Suite("New Thread launcher model")
struct NewThreadLauncherModelTests {
    private let connectionA = UUID()
    private let connectionB = UUID()

    private func option(
        _ name: String,
        connectionID: UUID,
        connectionName: String = "Local",
        directory: String = "/home/dev",
        label: String? = nil
    ) -> ProjectOption {
        let project = Project(
            id: name,
            name: name,
            defaultWorkingDirectory: directory,
            createdAt: .distantPast,
            updatedAt: .distantPast
        )
        return ProjectOption(
            ref: ProjectRef(connectionID: connectionID, projectID: name),
            project: project,
            connectionName: connectionName,
            label: label ?? name
        )
    }

    private func agent(_ id: AgentID, available: Bool, reason: String? = nil) -> Agent {
        Agent(
            id: id,
            available: available,
            reason: reason
        )
    }

    // MARK: - Filtering and ordering

    @Test("empty query orders the contextual project first, the rest alphabetical")
    func emptyQueryOrdering() {
        let options = [
            option("website", connectionID: connectionA),
            option("atc", connectionID: connectionA),
            option("t3code", connectionID: connectionA),
        ]
        let rows = NewThreadLauncherModel.rows(
            options: options,
            query: "",
            contextProject: ProjectRef(connectionID: connectionA, projectID: "t3code")
        )
        #expect(rows.map(\.option.project.name) == ["t3code", "atc", "website"])
        #expect(rows.allSatisfy { $0.nameRanges.isEmpty })
    }

    @Test("no contextual project falls back to plain alphabetical")
    func alphabeticalWithoutContext() {
        let options = [
            option("website", connectionID: connectionA),
            option("atc", connectionID: connectionA),
        ]
        let rows = NewThreadLauncherModel.rows(options: options, query: "", contextProject: nil)
        #expect(rows.map(\.option.project.name) == ["atc", "website"])
    }

    @Test("query matches labels with highlight ranges")
    func labelMatch() throws {
        let options = [
            option("atc", connectionID: connectionA),
            option("website", connectionID: connectionA),
        ]
        let rows = NewThreadLauncherModel.rows(options: options, query: "atc", contextProject: nil)
        #expect(rows.map(\.option.project.name) == ["atc"])
        let row = try #require(rows.first)
        #expect(row.nameRanges.map { String(row.option.project.name[$0]) } == ["atc"])
    }

    @Test("connection name and working directory match as secondary text, after label matches")
    func secondaryMatch() {
        let options = [
            option("api", connectionID: connectionB, connectionName: "Production"),
            option("website", connectionID: connectionA, directory: "/srv/prod-web"),
            option("prod-tools", connectionID: connectionA),
        ]
        let rows = NewThreadLauncherModel.rows(options: options, query: "prod", contextProject: nil)
        #expect(rows.map(\.option.project.name) == ["prod-tools", "api", "website"])
        #expect(rows[1].nameRanges.isEmpty)
        #expect(rows[2].nameRanges.isEmpty)
    }

    @Test("a duplicate-name label's connection suffix never counts as a name match")
    func duplicateNameLabelSuffix() {
        // Sidebar labels disambiguate duplicates as "web · Production"; a
        // connection-name query must rank those as secondary, same as any
        // other connection match.
        let options = [
            option("web", connectionID: connectionA, label: "web · Local"),
            option(
                "web",
                connectionID: connectionB,
                connectionName: "Production",
                label: "web · Production"
            ),
            option("api", connectionID: connectionB, connectionName: "Production"),
        ]
        let rows = NewThreadLauncherModel.rows(
            options: options,
            query: "production",
            contextProject: nil
        )
        #expect(rows.map(\.option.connectionName) == ["Production", "Production"])
        #expect(rows.map(\.option.project.name) == ["api", "web"])
        #expect(rows.allSatisfy { $0.nameRanges.isEmpty })
    }

    @Test("a query with no matches returns no rows")
    func noMatches() {
        let options = [option("atc", connectionID: connectionA)]
        let rows = NewThreadLauncherModel.rows(options: options, query: "zzz", contextProject: nil)
        #expect(rows.isEmpty)
    }

    // MARK: - Keyboard navigation

    @Test("selection moves down, up, and wraps at both ends")
    func selectionMovement() {
        let rows = NewThreadLauncherModel.rows(
            options: [
                option("a", connectionID: connectionA),
                option("b", connectionID: connectionA),
                option("c", connectionID: connectionA),
            ],
            query: "",
            contextProject: nil
        )
        let a = rows[0].id, b = rows[1].id, c = rows[2].id
        #expect(NewThreadLauncherModel.movedSelection(from: a, by: 1, in: rows) == b)
        #expect(NewThreadLauncherModel.movedSelection(from: b, by: -1, in: rows) == a)
        #expect(NewThreadLauncherModel.movedSelection(from: c, by: 1, in: rows) == a)
        #expect(NewThreadLauncherModel.movedSelection(from: a, by: -1, in: rows) == c)
    }

    @Test("a missing selection enters from the end the motion came from")
    func selectionEntry() {
        let rows = NewThreadLauncherModel.rows(
            options: [
                option("a", connectionID: connectionA),
                option("b", connectionID: connectionA),
            ],
            query: "",
            contextProject: nil
        )
        #expect(NewThreadLauncherModel.movedSelection(from: nil, by: 1, in: rows) == rows.first?.id)
        #expect(NewThreadLauncherModel.movedSelection(from: nil, by: -1, in: rows) == rows.last?.id)
        #expect(NewThreadLauncherModel.movedSelection(from: nil, by: 1, in: []) == nil)
    }

    // MARK: - Agent rules

    @Test("an available current agent is preserved across project changes")
    func agentPreserved() {
        let agents = [agent(.codex, available: true), agent(.claudeCode, available: true)]
        #expect(NewThreadLauncherModel.resolvedAgent(preferring: .claudeCode, in: agents) == .claudeCode)
    }

    @Test("an unavailable current agent adopts the first available one")
    func agentAdopted() {
        let agents = [
            agent(.codex, available: false, reason: "Install codex"),
            agent(.claudeCode, available: true),
        ]
        #expect(NewThreadLauncherModel.resolvedAgent(preferring: .codex, in: agents) == .claudeCode)
    }

    @Test("with nothing available the current selection stands")
    func nothingAvailable() {
        let agents = [
            agent(.codex, available: false),
            agent(.claudeCode, available: false),
        ]
        #expect(NewThreadLauncherModel.resolvedAgent(preferring: .claudeCode, in: agents) == .claudeCode)
    }

    // MARK: - Last-used persistence

    @Test("a stored last-used agent wins when still available")
    func lastUsedWins() {
        let agents = [agent(.codex, available: true), agent(.claudeCode, available: true)]
        #expect(
            NewThreadLauncherModel.initialAgent(lastUsedRawValue: "claude-code", agents: agents)
                == .claudeCode
        )
    }

    @Test("an unavailable last-used agent falls back to the first available")
    func lastUsedUnavailable() {
        let agents = [
            agent(.codex, available: true),
            agent(.claudeCode, available: false, reason: "Install claude"),
        ]
        #expect(
            NewThreadLauncherModel.initialAgent(lastUsedRawValue: "claude-code", agents: agents)
                == .codex
        )
    }

    @Test("no stored value falls back to the first available agent")
    func noStoredValue() {
        let agents = [
            agent(.codex, available: false),
            agent(.claudeCode, available: true),
        ]
        #expect(
            NewThreadLauncherModel.initialAgent(lastUsedRawValue: "", agents: agents) == .claudeCode
        )
        #expect(
            NewThreadLauncherModel.initialAgent(lastUsedRawValue: "garbage", agents: []) == .codex
        )
    }
}

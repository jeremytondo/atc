import AppKit
import SwiftUI
import Testing
import ATCAPI
@testable import ATC

@MainActor
@Suite("Workspace startup configuration")
struct WorkspaceStartupTests {
    private func defaults() -> UserDefaults {
        let suite = "WorkspaceStartupTests.\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        defaults.removePersistentDomain(forName: suite)
        return defaults
    }

    private func configuredStore() -> (WorkspaceStartupStore, UUID, UUID) {
        let store = WorkspaceStartupStore(defaults: defaults())
        return (store, UUID(), UUID())
    }

    @Test("default invariant survives adds, transfer, and removals")
    func defaultInvariant() {
        var configuration = StartupConfiguration()
        let first = UUID()
        let second = UUID()

        configuration.add(target: .shell, id: first)
        #expect(configuration.defaultEntryID == first)

        configuration.add(target: .action(id: "act_codex"), id: second)
        #expect(configuration.defaultEntryID == first)

        configuration.transferDefault(to: second)
        #expect(configuration.defaultEntryID == second)

        configuration.remove(id: second)
        #expect(configuration.defaultEntryID == first)

        configuration.remove(id: first)
        #expect(configuration == .empty)
    }

    @Test("duplicate targets retain independent identity and names")
    func duplicateTargets() {
        var configuration = StartupConfiguration()
        let first = configuration.add(target: .action(id: "act_same"))
        let second = configuration.add(target: .action(id: "act_same"))

        configuration.setCustomName("One", for: first)
        configuration.setCustomName("Two", for: second)

        #expect(first != second)
        #expect(configuration.entries.map(\.customName) == ["One", "Two"])

        configuration.remove(id: first)
        #expect(configuration.entries.count == 1)
        #expect(configuration.entries[0].id == second)
        #expect(configuration.entries[0].customName == "Two")
        #expect(configuration.defaultEntryID == second)
    }

    @Test("persistence round-trips and garbage fails safe")
    func persistence() {
        let defaults = defaults()
        let connectionID = UUID()
        let projectID = "prj_one"
        let store = WorkspaceStartupStore(defaults: defaults)
        store.updateConnectionConfiguration(connectionID: connectionID) {
            $0.add(target: .shell)
            $0.add(target: .action(id: "act_codex"), customName: "Review")
        }
        store.setProjectMode(.custom, connectionID: connectionID, projectID: projectID)
        store.updateProjectConfiguration(connectionID: connectionID, projectID: projectID) {
            $0.removeAll()
        }

        let reloaded = WorkspaceStartupStore(defaults: defaults)
        #expect(reloaded.connectionConfiguration(connectionID: connectionID).entries.count == 2)
        #expect(reloaded.projectMode(connectionID: connectionID, projectID: projectID) == .custom)
        #expect(reloaded.resolvedConfiguration(
            connectionID: connectionID,
            projectID: projectID
        ) == .empty)

        defaults.set(Data("not-json".utf8), forKey: "workspaceStartup.v1")
        let corrupted = WorkspaceStartupStore(defaults: defaults)
        #expect(corrupted.connectionConfiguration(connectionID: connectionID) == .empty)
        #expect(corrupted.projectMode(
            connectionID: connectionID,
            projectID: projectID
        ) == .useConnectionDefaults)
    }

    @Test("Project inheritance is live until Custom copies once")
    func inheritance() {
        let (store, connectionID, _) = configuredStore()
        let projectID = "prj_one"

        store.updateConnectionConfiguration(connectionID: connectionID) {
            $0.add(target: .shell)
        }
        #expect(store.resolvedConfiguration(
            connectionID: connectionID,
            projectID: projectID
        ).entries.count == 1)

        store.updateConnectionConfiguration(connectionID: connectionID) {
            $0.add(target: .action(id: "act_live"))
        }
        #expect(store.resolvedConfiguration(
            connectionID: connectionID,
            projectID: projectID
        ).entries.count == 2)

        store.setProjectMode(.custom, connectionID: connectionID, projectID: projectID)
        let copied = store.resolvedConfiguration(
            connectionID: connectionID,
            projectID: projectID
        )
        store.updateConnectionConfiguration(connectionID: connectionID) {
            $0.add(target: .action(id: "act_later"))
        }
        #expect(store.resolvedConfiguration(
            connectionID: connectionID,
            projectID: projectID
        ) == copied)
    }

    @Test("custom empty suppresses defaults and returning to inheritance discards it")
    func explicitEmptyOverride() {
        let (store, connectionID, _) = configuredStore()
        let projectID = "prj_one"
        store.updateConnectionConfiguration(connectionID: connectionID) {
            $0.add(target: .shell)
        }
        store.setProjectMode(.custom, connectionID: connectionID, projectID: projectID)
        store.updateProjectConfiguration(connectionID: connectionID, projectID: projectID) {
            $0.removeAll()
        }

        #expect(store.projectMode(connectionID: connectionID, projectID: projectID) == .custom)
        #expect(store.resolvedConfiguration(
            connectionID: connectionID,
            projectID: projectID
        ) == .empty)

        store.setProjectMode(
            .useConnectionDefaults,
            connectionID: connectionID,
            projectID: projectID
        )
        #expect(store.resolvedConfiguration(
            connectionID: connectionID,
            projectID: projectID
        ).entries.count == 1)

        store.setProjectMode(.custom, connectionID: connectionID, projectID: projectID)
        #expect(store.resolvedConfiguration(
            connectionID: connectionID,
            projectID: projectID
        ).entries.count == 1)
    }

    @Test("cascade removals are scoped to the target")
    func cascadeRemovals() {
        let (store, connectionA, connectionB) = configuredStore()
        for connectionID in [connectionA, connectionB] {
            store.updateConnectionConfiguration(connectionID: connectionID) {
                $0.add(target: .shell)
            }
            store.setProjectMode(.custom, connectionID: connectionID, projectID: "keep")
            store.setProjectMode(.custom, connectionID: connectionID, projectID: "remove")
        }

        store.removeProject(connectionID: connectionA, projectID: "remove")
        #expect(store.projectMode(connectionID: connectionA, projectID: "remove")
            == .useConnectionDefaults)
        #expect(store.projectMode(connectionID: connectionA, projectID: "keep") == .custom)
        #expect(store.projectMode(connectionID: connectionB, projectID: "remove") == .custom)

        store.removeConnection(connectionID: connectionA)
        #expect(store.connectionConfiguration(connectionID: connectionA) == .empty)
        #expect(store.projectMode(connectionID: connectionA, projectID: "keep")
            == .useConnectionDefaults)
        #expect(store.connectionConfiguration(connectionID: connectionB).entries.count == 1)
        #expect(store.projectMode(connectionID: connectionB, projectID: "keep") == .custom)
    }

    @Test("AppModel clears an override after successful Project deletion")
    func appInitiatedProjectDelete() async throws {
        let defaults = defaults()
        let connections = ConnectionsStore(
            defaults: defaults,
            credentials: InMemoryCredentialStore()
        )
        let record = try connections.add(
            name: "Test",
            urlString: "http://test.example:7331",
            token: ""
        )
        let model = AppModel(
            connections: connections,
            clientFactory: { _ in MockATCClient(mockWorkspaces: []) },
            terminalRecoveryMonitor: .disabled(),
            workspaceStartupDefaults: defaults
        )
        model.workspaceStartup.setProjectMode(
            .custom,
            connectionID: record.id,
            projectID: "prj_notes"
        )

        try await model.deleteProject(ProjectRef(
            connectionID: record.id,
            projectID: "prj_notes"
        ))

        #expect(model.workspaceStartup.projectMode(
            connectionID: record.id,
            projectID: "prj_notes"
        ) == .useConnectionDefaults)
    }

    @Test("failed Project deletion preserves its override")
    func failedProjectDeletePreservesOverride() async throws {
        let defaults = defaults()
        let connections = ConnectionsStore(
            defaults: defaults,
            credentials: InMemoryCredentialStore()
        )
        let record = try connections.add(
            name: "Test",
            urlString: "http://test.example:7331",
            token: ""
        )
        let model = AppModel(
            connections: connections,
            clientFactory: { _ in MockATCClient() },
            terminalRecoveryMonitor: .disabled(),
            workspaceStartupDefaults: defaults
        )
        model.workspaceStartup.setProjectMode(
            .custom,
            connectionID: record.id,
            projectID: "prj_atelier"
        )

        await #expect(throws: ATCError.self) {
            try await model.deleteProject(ProjectRef(
                connectionID: record.id,
                projectID: "prj_atelier"
            ))
        }

        #expect(model.workspaceStartup.projectMode(
            connectionID: record.id,
            projectID: "prj_atelier"
        ) == .custom)
    }

    @Test("AppModel Connection removal cascades startup preferences")
    func appModelConnectionCascade() throws {
        let defaults = defaults()
        let connections = ConnectionsStore(
            defaults: defaults,
            credentials: InMemoryCredentialStore()
        )
        let record = try connections.add(
            name: "Test",
            urlString: "http://test.example:7331",
            token: ""
        )
        let model = AppModel(
            connections: connections,
            clientFactory: { _ in MockATCClient() },
            terminalRecoveryMonitor: .disabled(),
            workspaceStartupDefaults: defaults
        )
        model.workspaceStartup.updateConnectionConfiguration(connectionID: record.id) {
            $0.add(target: .shell)
        }
        model.workspaceStartup.setProjectMode(
            .custom,
            connectionID: record.id,
            projectID: "prj_one"
        )

        model.removeConnection(id: record.id)

        #expect(model.workspaceStartup.connectionConfiguration(connectionID: record.id) == .empty)
        #expect(model.workspaceStartup.projectMode(
            connectionID: record.id,
            projectID: "prj_one"
        ) == .useConnectionDefaults)
    }
}

@MainActor
@Suite("Workspace startup validation")
struct WorkspaceStartupValidationTests {
    private let enabled = ATCAction(
        id: "act_enabled",
        name: "Enabled",
        description: nil,
        enabled: true,
        command: "enabled",
        args: [],
        isAgent: true
    )
    private let disabled = ATCAction(
        id: "act_disabled",
        name: "Disabled",
        description: nil,
        enabled: false,
        command: "disabled",
        args: [],
        isAgent: false
    )

    private func configuration() -> StartupConfiguration {
        var configuration = StartupConfiguration()
        configuration.add(target: .action(id: enabled.id))
        configuration.add(target: .action(id: disabled.id))
        configuration.add(target: .action(id: "act_missing"))
        configuration.add(target: .shell)
        return configuration
    }

    @Test("loaded reachable registry distinguishes all entry statuses")
    func loadedMatrix() {
        let result = StartupEntryValidator.validate(
            configuration: configuration(),
            actions: [enabled, disabled],
            hasLoadedOnce: true,
            isReachable: true
        )

        #expect(result.canEdit)
        #expect(result.entries.map(\.availability) == [
            .valid, .disabled, .missing, .valid,
        ])
        #expect(result.entries[0].cachedActionName == "Enabled")
        #expect(result.entries[1].cachedActionName == "Disabled")
    }

    @Test("unloaded or unreachable registry cannot validate any target")
    func unableMatrix() {
        for snapshot in [(false, false), (false, true), (true, false)] {
            let result = StartupEntryValidator.validate(
                configuration: configuration(),
                actions: [enabled, disabled],
                hasLoadedOnce: snapshot.0,
                isReachable: snapshot.1
            )
            #expect(!result.canEdit)
            #expect(result.entries.allSatisfy {
                $0.availability == .unableToValidate
            })
        }
    }

    @Test("summary formats empty, singular, plural, and unavailable defaults")
    func summaries() {
        #expect(WorkspaceStartupSummary.text(
            configuration: .empty,
            actions: [enabled],
            hasLoadedOnce: true
        ) == "None configured")

        var configuration = StartupConfiguration()
        configuration.add(target: .shell)
        #expect(WorkspaceStartupSummary.text(
            configuration: configuration,
            actions: [enabled],
            hasLoadedOnce: true
        ) == "1 Session · Default: Shell")

        let actionID = configuration.add(target: .action(id: enabled.id))
        configuration.transferDefault(to: actionID)
        #expect(WorkspaceStartupSummary.text(
            configuration: configuration,
            actions: [enabled],
            hasLoadedOnce: true
        ) == "2 Sessions · Default: Enabled")

        #expect(WorkspaceStartupSummary.text(
            configuration: configuration,
            actions: [],
            hasLoadedOnce: true
        ) == "2 Sessions · Default: Unavailable Action")

        // Before the registry has loaded, an unknown Action is unvalidated
        // rather than missing — mirror the editor instead of alarming.
        #expect(WorkspaceStartupSummary.text(
            configuration: configuration,
            actions: [],
            hasLoadedOnce: false
        ) == "2 Sessions · Default: Action")
    }
}

@MainActor
@Suite("Workspace startup editor hosting smoke")
struct WorkspaceStartupEditorHostingSmokeTests {
    @Test("Connection and Project editors host without crashing")
    func hostEditors() async throws {
        let model = AppModel.preview()
        await model.refreshAll()
        let connectionID = try #require(model.runtimes.first?.id)
        model.workspaceStartup.updateConnectionConfiguration(connectionID: connectionID) {
            $0.add(target: .shell)
            $0.add(target: .action(id: "act_fh9g7e6571qo53r0t647ughtfg"))
        }

        for target in [
            WorkspaceStartupEditorTarget.connection(connectionID),
            .project(ProjectRef(connectionID: connectionID, projectID: "prj_atelier")),
        ] {
            let window = NSWindow(
                contentRect: NSRect(x: 0, y: 0, width: 580, height: 520),
                styleMask: [.titled],
                backing: .buffered,
                defer: false
            )
            window.contentView = NSHostingView(
                rootView: WorkspaceStartupEditorSheet(target: target)
                    .environment(model)
            )
            window.orderFront(nil)
            pump(seconds: 0.25)
            window.orderOut(nil)
        }
    }
}

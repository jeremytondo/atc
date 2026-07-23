import Foundation
import Testing
import ATCAPI
@testable import ATC

@MainActor
private final class WorkspaceStartupRecorder {
    enum StartResult {
        case success
        case definitiveFailure
        case ambiguousFailure
    }

    var events: [String] = []
    var requests: [StartSessionRequest] = []
    var results: [String: [StartResult]] = [:]
    var heldNames: Set<String> = []
    var refreshCount = 0
    var holdCreate = false

    private var createContinuation: CheckedContinuation<Void, Never>?
    private var startContinuations: [CheckedContinuation<Void, Never>] = []

    func create(projectID: String, name: String) async throws -> Workspace {
        events.append("create")
        if holdCreate {
            await withCheckedContinuation { createContinuation = $0 }
        }
        return Workspace(
            id: "wsp_created",
            projectId: projectID,
            name: name,
            createdAt: .now,
            updatedAt: .now
        )
    }

    func start(_ request: StartSessionRequest) async throws -> Session {
        requests.append(request)
        let key = request.name ?? request.actionId ?? "Shell"
        events.append("start:\(key)")
        if heldNames.contains(key) {
            await withCheckedContinuation { startContinuations.append($0) }
        }

        let result: StartResult
        if var queued = results[key], !queued.isEmpty {
            result = queued.removeFirst()
            results[key] = queued
        } else {
            result = .success
        }
        switch result {
        case .success:
            return Session(
                id: "ses_\(requests.count)",
                name: request.name,
                actionId: request.actionId,
                actionName: request.actionId,
                isAgent: request.actionId != nil,
                workingDir: "/tmp",
                status: .live,
                createdAt: .now,
                updatedAt: .now,
                workspace: SessionWorkspace(id: request.workspaceId, name: "Created")
            )
        case .definitiveFailure:
            throw ATCError.api(
                code: "launch_failed",
                message: "launch failed",
                sessionID: nil
            )
        case .ambiguousFailure:
            throw URLError(.timedOut)
        }
    }

    func refresh() async {
        refreshCount += 1
        events.append("refresh")
    }

    func releaseCreate() {
        holdCreate = false
        createContinuation?.resume()
        createContinuation = nil
    }

    func releaseStarts() {
        heldNames = []
        let continuations = startContinuations
        startContinuations = []
        continuations.forEach { $0.resume() }
    }
}

/// Yields the cooperative MainActor executor until `condition` holds.
/// Deterministic replacement for run-loop pumping here: nested
/// `RunLoop.run` can starve queued MainActor tasks, while yielding always
/// drains them.
@MainActor
private func settle(until condition: () -> Bool) async {
    var iterations = 0
    while !condition(), iterations < 10_000 {
        iterations += 1
        await Task.yield()
    }
}

@MainActor
@Suite("Workspace startup coordinator")
struct WorkspaceStartupCoordinatorTests {
    private let connectionID = UUID()

    private func action(_ id: String, name: String, enabled: Bool = true) -> ATCAction {
        ATCAction(
            id: id,
            name: name,
            description: nil,
            enabled: enabled,
            command: id,
            args: [],
            isAgent: true
        )
    }

    private func plan(
        configuration: StartupConfiguration,
        actions: [ATCAction] = []
    ) throws -> WorkspaceStartupLaunchPlan {
        let validation = StartupEntryValidator.validate(
            configuration: configuration,
            actions: actions,
            hasLoadedOnce: true,
            isReachable: true
        )
        return try #require(WorkspaceStartupLaunchPlan(
            configuration: configuration,
            validation: validation
        ))
    }

    private func coordinator(
        recorder: WorkspaceStartupRecorder,
        plan: WorkspaceStartupLaunchPlan,
        activations: @escaping (WorkspaceRef, SessionRef?) -> Void = { _, _ in },
        notices: @escaping (StartupNotice) -> Void = { _ in }
    ) -> WorkspaceStartupCoordinator {
        WorkspaceStartupCoordinator(
            connectionID: connectionID,
            plan: plan,
            operations: .init(
                createWorkspace: recorder.create,
                startSession: recorder.start,
                refreshSessions: recorder.refresh
            ),
            onActivate: activations,
            onNotice: notices
        )
    }

    @Test("default launches first, activation selects it, then companions launch concurrently")
    func happyPath() async throws {
        var configuration = StartupConfiguration()
        configuration.add(target: .shell, customName: "Default")
        configuration.add(target: .action(id: "act_one"), customName: "One")
        configuration.add(target: .action(id: "act_two"), customName: "Two")
        let recorder = WorkspaceStartupRecorder()
        recorder.heldNames = ["One", "Two"]
        var selected: SessionRef?
        let coordinator = coordinator(
            recorder: recorder,
            plan: try plan(
                configuration: configuration,
                actions: [action("act_one", name: "One"), action("act_two", name: "Two")]
            ),
            activations: { _, session in
                recorder.events.append("activate")
                selected = session
            }
        )

        await coordinator.start(projectID: "prj_one", name: "Created")
        await settle(until: { recorder.requests.count == 3 })

        #expect(coordinator.state == .succeeded)
        #expect(selected?.sessionID == "ses_1")
        #expect(recorder.events == [
            "create", "start:Default", "activate", "start:One", "start:Two",
        ])
        // Both companion calls entered before either was allowed to finish.
        #expect(recorder.requests.map(\.name) == ["Default", "One", "Two"])
        recorder.releaseStarts()
    }

    @Test("definitive failure retries only the default and is repeatable")
    func definitiveRetry() async throws {
        var configuration = StartupConfiguration()
        configuration.add(target: .shell, customName: "Default")
        configuration.add(target: .shell, customName: "Companion")
        let recorder = WorkspaceStartupRecorder()
        recorder.results["Default"] = [
            .definitiveFailure, .definitiveFailure, .success,
        ]
        var activation: (WorkspaceRef, SessionRef?)?
        let coordinator = coordinator(
            recorder: recorder,
            plan: try plan(configuration: configuration),
            activations: { activation = ($0, $1) }
        )

        await coordinator.start(projectID: "prj_one", name: "Created")
        #expect(coordinator.state == .failedDefault(message: "launch failed"))
        #expect(coordinator.primaryActionTitle == "Retry")
        #expect(recorder.requests.map(\.name) == ["Default"])

        await coordinator.performPrimaryAction()
        #expect(coordinator.state == .failedDefault(message: "launch failed"))
        #expect(recorder.requests.map(\.name) == ["Default", "Default"])

        await coordinator.performPrimaryAction()
        #expect(coordinator.state == .succeeded)
        #expect(activation?.1 != nil)
        await settle(until: { recorder.requests.count == 4 })
        #expect(recorder.requests.map(\.name) == [
            "Default", "Default", "Default", "Companion",
        ])
    }

    @Test("Open Workspace Anyway activates without selection and starts companions")
    func openAnyway() async throws {
        var configuration = StartupConfiguration()
        configuration.add(target: .shell, customName: "Default")
        configuration.add(target: .shell, customName: "Companion")
        let recorder = WorkspaceStartupRecorder()
        recorder.results["Default"] = [.definitiveFailure]
        var activation: (WorkspaceRef, SessionRef?)?
        let coordinator = coordinator(
            recorder: recorder,
            plan: try plan(configuration: configuration),
            activations: { activation = ($0, $1) }
        )

        await coordinator.start(projectID: "prj_one", name: "Created")
        #expect(coordinator.secondaryActionTitle == "Open Workspace Anyway")
        coordinator.performSecondaryAction()
        await settle(until: { recorder.requests.count == 2 })

        #expect(activation?.0.workspaceID == "wsp_created")
        #expect(activation?.1 == nil)
        #expect(recorder.requests.map(\.name) == ["Default", "Companion"])
    }

    @Test("transport ambiguity refreshes, never retries, and opens without selection")
    func ambiguous() async throws {
        var configuration = StartupConfiguration()
        configuration.add(target: .shell, customName: "Default")
        configuration.add(target: .shell, customName: "Companion")
        let recorder = WorkspaceStartupRecorder()
        recorder.results["Default"] = [.ambiguousFailure]
        var activation: (WorkspaceRef, SessionRef?)?
        let coordinator = coordinator(
            recorder: recorder,
            plan: try plan(configuration: configuration),
            activations: { activation = ($0, $1) }
        )

        await coordinator.start(projectID: "prj_one", name: "Created")
        #expect(recorder.refreshCount == 1)
        #expect(recorder.requests.map(\.name) == ["Default"])
        if case .ambiguous = coordinator.state {
            // Expected.
        } else {
            Issue.record("Expected ambiguous state")
        }

        // Ambiguity offers no Retry and no Open Workspace Anyway.
        #expect(coordinator.secondaryActionTitle == nil)
        coordinator.performSecondaryAction()
        #expect(recorder.requests.map(\.name) == ["Default"])
        #expect(activation == nil)

        #expect(coordinator.primaryActionTitle == "Open Workspace")
        await coordinator.performPrimaryAction()
        await settle(until: { recorder.requests.count == 2 })
        #expect(activation?.1 == nil)
        #expect(recorder.requests.map(\.name) == ["Default", "Companion"])
    }

    @Test("background failures aggregate, unavailable entries skip, and successes remain")
    func backgroundAggregation() async throws {
        var configuration = StartupConfiguration()
        configuration.add(target: .shell, customName: "Default")
        configuration.add(target: .action(id: "act_editor"))
        configuration.add(target: .action(id: "act_editor"))
        configuration.add(target: .action(id: "act_missing"))
        configuration.add(target: .action(id: "act_success"), customName: "Healthy")
        let recorder = WorkspaceStartupRecorder()
        recorder.results["act_editor"] = [.definitiveFailure, .definitiveFailure]
        var notices: [StartupNotice] = []
        let coordinator = coordinator(
            recorder: recorder,
            plan: try plan(
                configuration: configuration,
                actions: [
                    action("act_editor", name: "Editor"),
                    action("act_success", name: "Success"),
                ]
            ),
            notices: { notices.append($0) }
        )

        await coordinator.start(projectID: "prj_one", name: "Created")
        await settle(until: { notices.count == 1 })

        #expect(recorder.requests.count == 4)
        #expect(!recorder.requests.contains { $0.actionId == "act_missing" })
        #expect(recorder.requests.contains { $0.name == "Healthy" })
        #expect(notices.count == 1)
        #expect(notices.first?.workspaceName == "Created")
        #expect(notices.first?.messages == [
            "2 × Editor failed: launch_failed",
            "Action skipped: unavailable",
        ])
    }

    @Test("all successful background launches produce no notice")
    func noNotice() async throws {
        var configuration = StartupConfiguration()
        configuration.add(target: .shell, customName: "Default")
        configuration.add(target: .shell, customName: "Companion")
        let recorder = WorkspaceStartupRecorder()
        var notices: [StartupNotice] = []
        let coordinator = coordinator(
            recorder: recorder,
            plan: try plan(configuration: configuration),
            notices: { notices.append($0) }
        )

        await coordinator.start(projectID: "prj_one", name: "Created")
        await coordinator.backgroundTask?.value
        #expect(recorder.requests.count == 2)
        #expect(notices.isEmpty)
    }

    @Test("sheet dismissal is blocked during progress and restored after failure")
    func dismissalPresentation() async throws {
        var configuration = StartupConfiguration()
        configuration.add(target: .shell, customName: "Default")
        let recorder = WorkspaceStartupRecorder()
        recorder.holdCreate = true
        recorder.results["Default"] = [.definitiveFailure]
        let coordinator = coordinator(
            recorder: recorder,
            plan: try plan(configuration: configuration)
        )

        let start = Task {
            await coordinator.start(projectID: "prj_one", name: "Created")
        }
        await Task.yield()
        #expect(coordinator.state == .creatingWorkspace)
        #expect(coordinator.isDismissDisabled)

        recorder.releaseCreate()
        await start.value
        #expect(!coordinator.isDismissDisabled)
        #expect(coordinator.state == .failedDefault(message: "launch failed"))
    }
}

@MainActor
@Suite("Workspace startup creation cue")
struct WorkspaceStartupCreationCueTests {
    private func cue(
        _ configuration: StartupConfiguration,
        availability: StartupEntryAvailability?
    ) -> WorkspaceStartupCreationCue {
        let entries = configuration.entries.map {
            ValidatedStartupEntry(
                entryID: $0.id,
                availability: availability ?? .unableToValidate,
                cachedActionName: nil
            )
        }
        return WorkspaceStartupCreationCue.resolve(
            configuration: configuration,
            validation: StartupConfigurationValidation(entries: entries, canEdit: true)
        )
    }

    @Test("empty, valid, unavailable, and unvalidated configurations choose the right cue")
    func cueMatrix() {
        #expect(cue(.empty, availability: nil) == .none)

        var configuration = StartupConfiguration()
        configuration.add(target: .action(id: "act_default"))
        #expect(cue(configuration, availability: .valid) == .configured)
        #expect(cue(configuration, availability: .missing) == .defaultUnavailable)
        #expect(cue(configuration, availability: .disabled) == .defaultUnavailable)
        #expect(cue(configuration, availability: .unableToValidate) == .configured)

        configuration.add(target: .action(id: "act_background"))
        let mixedValidation = StartupConfigurationValidation(
            entries: [
                ValidatedStartupEntry(
                    entryID: configuration.entries[0].id,
                    availability: .valid,
                    cachedActionName: "Default"
                ),
                ValidatedStartupEntry(
                    entryID: configuration.entries[1].id,
                    availability: .missing,
                    cachedActionName: nil
                ),
            ],
            canEdit: true
        )
        #expect(WorkspaceStartupCreationCue.resolve(
            configuration: configuration,
            validation: mixedValidation
        ) == .configured)
    }

    @Test("an empty configuration has no launch plan")
    func emptyPlan() {
        #expect(WorkspaceStartupLaunchPlan(
            configuration: .empty,
            validation: StartupConfigurationValidation(entries: [], canEdit: false)
        ) == nil)
    }

    @Test("activating an existing Workspace never reruns startup")
    func reopeningDoesNotLaunch() async throws {
        let client = ScriptableClient()
        let appModel = AppModel.preview(client: client)
        await appModel.refreshAll()
        let runtime = try #require(appModel.runtimes.first)
        appModel.workspaceStartup.updateConnectionConfiguration(connectionID: runtime.id) {
            $0.add(target: .shell)
        }
        let state = WindowState.ephemeral()

        #expect(state.activateWorkspace(
            WorkspaceRef(connectionID: runtime.id, workspaceID: "wsp_parser"),
            in: appModel
        ))
        pump(seconds: 0.05)

        #expect(client.startSessionRequests.isEmpty)
    }
}

import Foundation
import Observation
import ATCAPI

enum WorkspaceStartupCreationCue: Equatable {
    case none
    case configured
    case defaultUnavailable

    static func resolve(
        configuration: StartupConfiguration,
        validation: StartupConfigurationValidation
    ) -> Self {
        guard !configuration.entries.isEmpty,
              let defaultID = configuration.defaultEntryID
        else { return .none }

        switch validation.entry(id: defaultID)?.availability {
        case .disabled, .missing:
            return .defaultUnavailable
        case .valid, .unableToValidate, nil:
            return .configured
        }
    }
}

struct StartupNotice: Identifiable, Equatable {
    let id: UUID
    let workspaceName: String
    let messages: [String]

    init(id: UUID = UUID(), workspaceName: String, messages: [String]) {
        self.id = id
        self.workspaceName = workspaceName
        self.messages = messages
    }
}

struct WorkspaceStartupLaunch: Equatable, Sendable {
    let entryID: UUID
    let target: StartupEntry.Target
    let customName: String?
    let identity: String
    let availability: StartupEntryAvailability

    var displayName: String {
        guard let customName else { return identity }
        return "\(identity) · \(customName)"
    }

    func request(workspaceID: String) -> StartSessionRequest {
        let actionID: String?
        switch target {
        case .action(let id): actionID = id
        case .shell: actionID = nil
        }
        return StartSessionRequest(
            workspaceId: workspaceID,
            actionId: actionID,
            name: customName
        )
    }
}

struct WorkspaceStartupLaunchPlan: Equatable, Sendable {
    let defaultLaunch: WorkspaceStartupLaunch
    let backgroundLaunches: [WorkspaceStartupLaunch]

    init?(
        configuration: StartupConfiguration,
        validation: StartupConfigurationValidation
    ) {
        guard let defaultID = configuration.defaultEntryID,
              let defaultEntry = configuration.entries.first(where: {
                  $0.id == defaultID
              })
        else { return nil }

        func launch(for entry: StartupEntry) -> WorkspaceStartupLaunch {
            let validated = validation.entry(id: entry.id)
            let identity: String
            switch entry.target {
            case .shell:
                identity = "Shell"
            case .action:
                identity = validated?.cachedActionName ?? "Action"
            }
            return WorkspaceStartupLaunch(
                entryID: entry.id,
                target: entry.target,
                customName: entry.customName,
                identity: identity,
                availability: validated?.availability ?? .unableToValidate
            )
        }

        defaultLaunch = launch(for: defaultEntry)
        backgroundLaunches = configuration.entries
            .filter { $0.id != defaultID }
            .map(launch)
    }
}

@MainActor
@Observable
final class WorkspaceStartupCoordinator {
    enum State: Equatable {
        case idle
        case creatingWorkspace
        case launchingDefault(identity: String)
        case succeeded
        case failedCreation(message: String)
        // Definitive server-reported failure; transport-level uncertainty is
        // the separate `ambiguous` case.
        case failedDefault(message: String)
        case ambiguous(message: String)
    }

    struct Operations {
        var createWorkspace: (_ projectID: String, _ name: String) async throws -> Workspace
        var startSession: (_ request: StartSessionRequest) async throws -> Session
        var refreshSessions: () async -> Void
    }

    enum BackgroundIssue: Equatable {
        case skipped(identity: String)
        case failed(identity: String, reason: String)

        var message: String {
            switch self {
            case .skipped(let identity):
                return "\(identity) skipped: unavailable"
            case .failed(let identity, let reason):
                return "\(identity) failed: \(reason)"
            }
        }
    }

    private(set) var state: State = .idle
    private(set) var workspaceRef: WorkspaceRef?

    let connectionID: UUID
    let plan: WorkspaceStartupLaunchPlan

    @ObservationIgnored private let operations: Operations
    @ObservationIgnored private let onActivate: (WorkspaceRef, SessionRef?) -> Void
    @ObservationIgnored private let onNotice: (StartupNotice) -> Void
    @ObservationIgnored private var workspaceName = ""
    /// The one background-launch task; also the "all launches settled" signal.
    @ObservationIgnored private(set) var backgroundTask: Task<Void, Never>?

    init(
        connectionID: UUID,
        plan: WorkspaceStartupLaunchPlan,
        operations: Operations,
        onActivate: @escaping (WorkspaceRef, SessionRef?) -> Void,
        onNotice: @escaping (StartupNotice) -> Void
    ) {
        self.connectionID = connectionID
        self.plan = plan
        self.operations = operations
        self.onActivate = onActivate
        self.onNotice = onNotice
    }

    var isInProgress: Bool {
        switch state {
        case .creatingWorkspace, .launchingDefault:
            return true
        case .idle, .succeeded, .failedCreation, .failedDefault, .ambiguous:
            return false
        }
    }

    var isDismissDisabled: Bool { isInProgress }

    var progressLabel: String? {
        switch state {
        case .creatingWorkspace:
            return "Creating Workspace…"
        case .launchingDefault(let identity):
            return "Starting \(identity)…"
        case .idle, .succeeded, .failedCreation, .failedDefault, .ambiguous:
            return nil
        }
    }

    var errorMessage: String? {
        switch state {
        case .failedCreation(let message):
            return message
        case .failedDefault(let message):
            return message
        case .ambiguous(let message):
            return "Default Session startup status could not be confirmed. \(message)"
        case .idle, .creatingWorkspace, .launchingDefault, .succeeded:
            return nil
        }
    }

    func start(projectID: String, name: String) async {
        guard workspaceRef == nil, !isInProgress else { return }
        workspaceName = name
        state = .creatingWorkspace
        do {
            let workspace = try await operations.createWorkspace(projectID, name)
            workspaceRef = WorkspaceRef(
                connectionID: connectionID,
                workspaceID: workspace.id
            )
            await launchDefault()
        } catch {
            state = .failedCreation(message: error.localizedDescription)
        }
    }

    /// Title of the sheet's primary action once a result state is reached;
    /// nil while the initial Create Workspace submit still belongs to the
    /// sheet itself.
    var primaryActionTitle: String? {
        switch state {
        case .failedDefault:
            return "Retry"
        case .ambiguous:
            return "Open Workspace"
        case .idle, .creatingWorkspace, .launchingDefault, .succeeded,
             .failedCreation:
            return nil
        }
    }

    var secondaryActionTitle: String? {
        guard case .failedDefault = state else { return nil }
        return "Open Workspace Anyway"
    }

    func performPrimaryAction() async {
        switch state {
        case .failedDefault:
            await launchDefault()
        case .ambiguous:
            activateWithoutSelectionAndLaunchBackground()
        case .idle, .creatingWorkspace, .launchingDefault, .succeeded,
             .failedCreation:
            break
        }
    }

    func performSecondaryAction() {
        guard case .failedDefault = state else { return }
        activateWithoutSelectionAndLaunchBackground()
    }

    static func isDefinitiveDefaultFailure(_ error: any Error) -> Bool {
        guard let error = error as? ATCError else { return false }
        if case .api = error { return true }
        return false
    }

    static func backgroundFailureReason(_ error: any Error) -> String {
        if let error = error as? ATCError, let code = error.apiCode {
            return code
        }
        return error.localizedDescription
    }

    static func notice(
        workspaceName: String,
        issues: [BackgroundIssue]
    ) -> StartupNotice? {
        guard !issues.isEmpty else { return nil }
        var orderedMessages: [String] = []
        var counts: [String: Int] = [:]
        for issue in issues {
            let message = issue.message
            if counts[message] == nil {
                orderedMessages.append(message)
            }
            counts[message, default: 0] += 1
        }
        let messages = orderedMessages.map { message in
            let count = counts[message, default: 1]
            return count == 1 ? message : "\(count) × \(message)"
        }
        return StartupNotice(workspaceName: workspaceName, messages: messages)
    }

    private func launchDefault() async {
        guard let workspaceRef else { return }
        state = .launchingDefault(identity: plan.defaultLaunch.displayName)
        do {
            let session = try await operations.startSession(
                plan.defaultLaunch.request(workspaceID: workspaceRef.workspaceID)
            )
            state = .succeeded
            onActivate(
                workspaceRef,
                SessionRef(connectionID: connectionID, sessionID: session.id)
            )
            startBackgroundLaunches()
        } catch {
            let message = error.localizedDescription
            if Self.isDefinitiveDefaultFailure(error) {
                state = .failedDefault(message: message)
            } else {
                state = .ambiguous(message: message)
                await operations.refreshSessions()
            }
        }
    }

    private func activateWithoutSelectionAndLaunchBackground() {
        guard let workspaceRef else { return }
        state = .succeeded
        onActivate(workspaceRef, nil)
        startBackgroundLaunches()
    }

    private func startBackgroundLaunches() {
        guard backgroundTask == nil, let workspaceRef else { return }
        let launches = plan.backgroundLaunches
        let workspaceName = workspaceName
        backgroundTask = Task { [self] in
            let issues = await launchBackground(
                launches,
                workspaceID: workspaceRef.workspaceID
            )
            if let notice = Self.notice(workspaceName: workspaceName, issues: issues) {
                onNotice(notice)
            }
        }
    }

    private func launchBackground(
        _ launches: [WorkspaceStartupLaunch],
        workspaceID: String
    ) async -> [BackgroundIssue] {
        // Slot per launch so concurrent completions keep configuration order.
        var issues: [BackgroundIssue?] = Array(repeating: nil, count: launches.count)
        var tasks: [(Int, Task<BackgroundIssue?, Never>)] = []

        for (index, launch) in launches.enumerated() {
            switch launch.availability {
            case .disabled, .missing:
                issues[index] = .skipped(identity: launch.displayName)
            case .valid, .unableToValidate:
                let startSession = operations.startSession
                tasks.append((index, Task { @MainActor in
                    do {
                        _ = try await startSession(
                            launch.request(workspaceID: workspaceID)
                        )
                        return nil
                    } catch {
                        return .failed(
                            identity: launch.displayName,
                            reason: Self.backgroundFailureReason(error)
                        )
                    }
                }))
            }
        }

        for (index, task) in tasks {
            issues[index] = await task.value
        }
        return issues.compactMap(\.self)
    }
}

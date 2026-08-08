import Foundation
import ATCAppServerAPI
@testable import ATC

/// An in-memory App Server standing in for the generated `Client`. Tests seed
/// `projects`/`threads`/`terminals`/`agents`, then drive the real stores and
/// runtime against it; the semantic operations (list filtering, pin/archive,
/// idempotent thread-terminal open) mutate that model exactly as the contract
/// describes, so store behavior is exercised rather than restated.
///
/// Nonisolated and lock-guarded: `APIProtocol` is `Sendable`, the stores call
/// it from arbitrary executors, and tests read and write the model from the
/// main actor.
///
/// Every operation passes through `gate()`, which honors `delay` (racing a
/// slow response against a newer refresh) and `shouldFail` (driving
/// reachability and error paths). Reads snapshot the model *before* the gate,
/// so a parked request really does carry stale data. Operations no test needs
/// throw `StubUnimplemented` rather than returning a plausible-looking lie.
nonisolated final class ScriptableAppServerClient: APIProtocol, @unchecked Sendable {
    private let lock = NSLock()
    private var state = State()

    private struct State {
        var projects: [Project] = []
        var threads: [ATCThread] = []
        var terminals: [Terminal] = []
        var agents: [Agent] = []
        var shouldFail = false
        var delay: Duration?
        var directoryCheck: Components.Schemas.FsCheckResponse?
        var createdThreadRequests: [Components.Schemas.CreateThreadRequest] = []
        var createdTerminalRequests: [Components.Schemas.CreateTerminalRequest] = []
        var listProjectsCount = 0
        var listThreadsCount = 0
        var listTerminalsCount = 0
        var listAgentsCount = 0
        var openThreadTerminalCount = 0
        /// Server-assigned timestamps advance a whole second per write, so
        /// ordering assertions never depend on wall-clock resolution. The base
        /// sits well after `Fixtures.epoch` so anything the server creates
        /// sorts newer than anything a test seeded.
        var clock = Date(timeIntervalSince1970: 1_800_000_000)
        var idCounter = 0

        mutating func tick() -> Date {
            clock = clock.addingTimeInterval(1)
            return clock
        }

        mutating func nextID(_ prefix: String) -> String {
            idCounter += 1
            return "\(prefix)_\(idCounter)"
        }

        mutating func makeTerminal(
            projectId: String,
            threadId: String?,
            name: String?,
            command: [String]?,
            workingDirectory: String
        ) -> Terminal {
            let now = tick()
            return Terminal(
                id: nextID("trm"),
                projectId: projectId,
                threadId: threadId,
                name: name,
                command: command,
                initialWorkingDirectory: workingDirectory,
                status: .live,
                createdAt: now,
                updatedAt: now
            )
        }
    }

    init() {}

    // MARK: - Model

    var projects: [Project] {
        get { lock.withLock { state.projects } }
        set { lock.withLock { state.projects = newValue } }
    }

    var threads: [ATCThread] {
        get { lock.withLock { state.threads } }
        set { lock.withLock { state.threads = newValue } }
    }

    var terminals: [Terminal] {
        get { lock.withLock { state.terminals } }
        set { lock.withLock { state.terminals = newValue } }
    }

    var agents: [Agent] {
        get { lock.withLock { state.agents } }
        set { lock.withLock { state.agents = newValue } }
    }

    // MARK: - Behavior knobs

    /// Every operation throws while set.
    var shouldFail: Bool {
        get { lock.withLock { state.shouldFail } }
        set { lock.withLock { state.shouldFail = newValue } }
    }

    /// Parks every operation for this long before it resolves.
    var delay: Duration? {
        get { lock.withLock { state.delay } }
        set { lock.withLock { state.delay = newValue } }
    }

    /// Answer for `checkDirectory`; defaults to an available directory.
    var directoryCheck: Components.Schemas.FsCheckResponse? {
        get { lock.withLock { state.directoryCheck } }
        set { lock.withLock { state.directoryCheck = newValue } }
    }

    // MARK: - Captured requests and call counts

    var createdThreadRequests: [Components.Schemas.CreateThreadRequest] {
        lock.withLock { state.createdThreadRequests }
    }

    var createdTerminalRequests: [Components.Schemas.CreateTerminalRequest] {
        lock.withLock { state.createdTerminalRequests }
    }

    var listProjectsCount: Int { lock.withLock { state.listProjectsCount } }
    var listThreadsCount: Int { lock.withLock { state.listThreadsCount } }
    var listTerminalsCount: Int { lock.withLock { state.listTerminalsCount } }
    var listAgentsCount: Int { lock.withLock { state.listAgentsCount } }
    var openThreadTerminalCount: Int { lock.withLock { state.openThreadTerminalCount } }

    // MARK: - Projects

    func listProjects(_ input: Operations.ListProjects.Input) async throws
        -> Operations.ListProjects.Output {
        let payload = mutate { model -> [Project] in
            model.listProjectsCount += 1
            return model.projects.sorted { $0.createdAt > $1.createdAt }
        }
        try await gate()
        return .ok(.init(body: .json(payload)))
    }

    func createProject(_ input: Operations.CreateProject.Input) async throws
        -> Operations.CreateProject.Output {
        guard case .json(let request) = input.body else { throw StubUnimplemented("createProject") }
        try await gate()
        return .ok(.init(body: .json(mutate { model -> Project in
            let now = model.tick()
            let project = Project(
                id: model.nextID("prj"),
                name: request.name,
                defaultWorkingDirectory: request.defaultWorkingDirectory,
                createdAt: now,
                updatedAt: now
            )
            model.projects.append(project)
            return project
        })))
    }

    func getProject(_ input: Operations.GetProject.Input) async throws
        -> Operations.GetProject.Output {
        try await gate()
        let id = input.path.projectId
        guard let project = projects.first(where: { $0.id == id }) else {
            return .notFound(.init(body: .json(projectNotFound(id))))
        }
        return .ok(.init(body: .json(project)))
    }

    func updateProject(_ input: Operations.UpdateProject.Input) async throws
        -> Operations.UpdateProject.Output {
        guard case .json(let request) = input.body else { throw StubUnimplemented("updateProject") }
        try await gate()
        let id = input.path.projectId
        return mutate { model -> Operations.UpdateProject.Output in
            guard let index = model.projects.firstIndex(where: { $0.id == id }) else {
                return .notFound(.init(body: .json(projectNotFound(id))))
            }
            if let name = request.name { model.projects[index].name = name }
            if let directory = request.defaultWorkingDirectory {
                model.projects[index].defaultWorkingDirectory = directory
            }
            model.projects[index].updatedAt = model.tick()
            return .ok(.init(body: .json(model.projects[index])))
        }
    }

    func deleteProject(_ input: Operations.DeleteProject.Input) async throws
        -> Operations.DeleteProject.Output {
        try await gate()
        let id = input.path.projectId
        return mutate { model -> Operations.DeleteProject.Output in
            guard model.projects.contains(where: { $0.id == id }) else {
                return .notFound(.init(body: .json(projectNotFound(id))))
            }
            model.projects.removeAll { $0.id == id }
            return .noContent(.init())
        }
    }

    // MARK: - Terminals

    func listTerminals(_ input: Operations.ListTerminals.Input) async throws
        -> Operations.ListTerminals.Output {
        let projectID = input.query.projectId
        let payload = mutate { model -> [Terminal] in
            model.listTerminalsCount += 1
            return model.terminals
                .filter { projectID == nil || $0.projectId == projectID }
                .sorted { $0.createdAt > $1.createdAt }
        }
        try await gate()
        return .ok(.init(body: .json(payload)))
    }

    func createTerminal(_ input: Operations.CreateTerminal.Input) async throws
        -> Operations.CreateTerminal.Output {
        guard case .json(let request) = input.body else { throw StubUnimplemented("createTerminal") }
        try await gate()
        return mutate { model -> Operations.CreateTerminal.Output in
            model.createdTerminalRequests.append(request)
            guard let project = model.projects.first(where: { $0.id == request.projectId }) else {
                return .notFound(.init(body: .json(projectNotFound(request.projectId))))
            }
            let terminal = model.makeTerminal(
                projectId: project.id,
                threadId: nil,
                name: request.name,
                command: request.command,
                workingDirectory: request.workingDirectory ?? project.defaultWorkingDirectory
            )
            model.terminals.append(terminal)
            return .ok(.init(body: .json(terminal)))
        }
    }

    func getTerminal(_ input: Operations.GetTerminal.Input) async throws
        -> Operations.GetTerminal.Output {
        try await gate()
        let id = input.path.terminalId
        guard let terminal = terminals.first(where: { $0.id == id }) else {
            return .notFound(.init(body: .json(terminalNotFound(id))))
        }
        return .ok(.init(body: .json(terminal)))
    }

    func updateTerminal(_ input: Operations.UpdateTerminal.Input) async throws
        -> Operations.UpdateTerminal.Output {
        guard case .json(let request) = input.body else { throw StubUnimplemented("updateTerminal") }
        try await gate()
        let id = input.path.terminalId
        return mutate { model -> Operations.UpdateTerminal.Output in
            guard let index = model.terminals.firstIndex(where: { $0.id == id }) else {
                return .notFound(.init(body: .json(terminalNotFound(id))))
            }
            model.terminals[index].name = request.name
            model.terminals[index].updatedAt = model.tick()
            return .ok(.init(body: .json(model.terminals[index])))
        }
    }

    func deleteTerminal(_ input: Operations.DeleteTerminal.Input) async throws
        -> Operations.DeleteTerminal.Output {
        try await gate()
        let id = input.path.terminalId
        return mutate { model -> Operations.DeleteTerminal.Output in
            guard model.terminals.contains(where: { $0.id == id }) else {
                return .notFound(.init(body: .json(terminalNotFound(id))))
            }
            model.terminals.removeAll { $0.id == id }
            // The server drops a thread's link when its terminal goes away.
            for index in model.threads.indices where model.threads[index].linkedTerminalId == id {
                model.threads[index].linkedTerminalId = nil
            }
            return .noContent(.init())
        }
    }

    // MARK: - Threads

    func listThreads(_ input: Operations.ListThreads.Input) async throws
        -> Operations.ListThreads.Output {
        let wantsArchived = input.query.archived == ._true
        let projectID = input.query.projectId
        let payload = mutate { model -> [ATCThread] in
            model.listThreadsCount += 1
            let matching = model.threads
                .filter { $0.isArchived == wantsArchived }
                .filter { projectID == nil || $0.projectId == projectID }
            // Archived threads come back newest-archived-first; active threads
            // newest-created-first.
            return wantsArchived
                ? matching.sorted {
                    ($0.archivedAt ?? .distantPast) > ($1.archivedAt ?? .distantPast)
                }
                : matching.sorted { $0.createdAt > $1.createdAt }
        }
        try await gate()
        return .ok(.init(body: .json(payload)))
    }

    func createThread(_ input: Operations.CreateThread.Input) async throws
        -> Operations.CreateThread.Output {
        guard case .json(let request) = input.body else { throw StubUnimplemented("createThread") }
        try await gate()
        return mutate { model -> Operations.CreateThread.Output in
            model.createdThreadRequests.append(request)
            guard let project = model.projects.first(where: { $0.id == request.projectId }) else {
                return .notFound(.init(body: .json(projectNotFound(request.projectId))))
            }
            let now = model.tick()
            let thread = ATCThread(
                id: model.nextID("thr"),
                projectId: project.id,
                agentId: request.agentId,
                name: request.name,
                workingDirectory: request.workingDirectory ?? project.defaultWorkingDirectory,
                activityState: .idle,
                createdAt: now,
                updatedAt: now
            )
            model.threads.append(thread)
            return .ok(.init(body: .json(thread)))
        }
    }

    func getThread(_ input: Operations.GetThread.Input) async throws
        -> Operations.GetThread.Output {
        try await gate()
        let id = input.path.threadId
        guard let thread = threads.first(where: { $0.id == id }) else {
            return .notFound(.init(body: .json(threadNotFound(id))))
        }
        return .ok(.init(body: .json(thread)))
    }

    func updateThread(_ input: Operations.UpdateThread.Input) async throws
        -> Operations.UpdateThread.Output {
        guard case .json(let request) = input.body else { throw StubUnimplemented("updateThread") }
        try await gate()
        let id = input.path.threadId
        return mutate { model -> Operations.UpdateThread.Output in
            guard let index = model.threads.firstIndex(where: { $0.id == id }) else {
                return .notFound(.init(body: .json(threadNotFound(id))))
            }
            model.threads[index].name = request.name
            model.threads[index].updatedAt = model.tick()
            return .ok(.init(body: .json(model.threads[index])))
        }
    }

    func deleteThread(_ input: Operations.DeleteThread.Input) async throws
        -> Operations.DeleteThread.Output {
        try await gate()
        let id = input.path.threadId
        return mutate { model -> Operations.DeleteThread.Output in
            guard model.threads.contains(where: { $0.id == id }) else {
                return .notFound(.init(body: .json(threadNotFound(id))))
            }
            model.threads.removeAll { $0.id == id }
            model.terminals.removeAll { $0.threadId == id }
            return .noContent(.init())
        }
    }

    func archiveThread(_ input: Operations.ArchiveThread.Input) async throws
        -> Operations.ArchiveThread.Output {
        try await gate()
        let id = input.path.threadId
        return mutate { model -> Operations.ArchiveThread.Output in
            guard let index = model.threads.firstIndex(where: { $0.id == id }) else {
                return .notFound(.init(body: .json(threadNotFound(id))))
            }
            let now = model.tick()
            // Idempotent: an already-archived thread keeps its archive time.
            if model.threads[index].archivedAt == nil {
                model.threads[index].archivedAt = now
            }
            // Archiving drops the pin: an archived thread is never pinned.
            model.threads[index].pinnedAt = nil
            model.threads[index].updatedAt = now
            return .ok(.init(body: .json(model.threads[index])))
        }
    }

    func unarchiveThread(_ input: Operations.UnarchiveThread.Input) async throws
        -> Operations.UnarchiveThread.Output {
        try await gate()
        let id = input.path.threadId
        return mutate { model -> Operations.UnarchiveThread.Output in
            guard let index = model.threads.firstIndex(where: { $0.id == id }) else {
                return .notFound(.init(body: .json(threadNotFound(id))))
            }
            model.threads[index].archivedAt = nil
            model.threads[index].updatedAt = model.tick()
            return .ok(.init(body: .json(model.threads[index])))
        }
    }

    func pinThread(_ input: Operations.PinThread.Input) async throws
        -> Operations.PinThread.Output {
        try await gate()
        let id = input.path.threadId
        return mutate { model -> Operations.PinThread.Output in
            guard let index = model.threads.firstIndex(where: { $0.id == id }) else {
                return .notFound(.init(body: .json(threadNotFound(id))))
            }
            guard model.threads[index].archivedAt == nil else {
                return .conflict(.init(body: .json(.init(
                    _tag: .threadArchived,
                    threadId: id,
                    message: "Thread \(id) is archived"
                ))))
            }
            let now = model.tick()
            if model.threads[index].pinnedAt == nil {
                model.threads[index].pinnedAt = now
            }
            model.threads[index].updatedAt = now
            return .ok(.init(body: .json(model.threads[index])))
        }
    }

    func unpinThread(_ input: Operations.UnpinThread.Input) async throws
        -> Operations.UnpinThread.Output {
        try await gate()
        let id = input.path.threadId
        return mutate { model -> Operations.UnpinThread.Output in
            guard let index = model.threads.firstIndex(where: { $0.id == id }) else {
                return .notFound(.init(body: .json(threadNotFound(id))))
            }
            model.threads[index].pinnedAt = nil
            model.threads[index].updatedAt = model.tick()
            return .ok(.init(body: .json(model.threads[index])))
        }
    }

    /// Idempotent per the contract: a live linked terminal is returned as-is,
    /// otherwise a fresh live terminal is created and linked.
    func openThreadTerminal(_ input: Operations.OpenThreadTerminal.Input) async throws
        -> Operations.OpenThreadTerminal.Output {
        try await gate()
        let id = input.path.threadId
        return mutate { model -> Operations.OpenThreadTerminal.Output in
            model.openThreadTerminalCount += 1
            guard let index = model.threads.firstIndex(where: { $0.id == id }) else {
                return .notFound(.init(body: .json(threadNotFound(id))))
            }
            let thread = model.threads[index]
            if let linkedID = thread.linkedTerminalId,
               let existing = model.terminals.first(where: {
                   $0.id == linkedID && $0.status == .live
               }) {
                return .ok(.init(body: .json(existing)))
            }
            let terminal = model.makeTerminal(
                projectId: thread.projectId,
                threadId: thread.id,
                name: nil,
                command: nil,
                workingDirectory: thread.workingDirectory
            )
            model.terminals.append(terminal)
            model.threads[index].linkedTerminalId = terminal.id
            model.threads[index].updatedAt = terminal.createdAt
            return .ok(.init(body: .json(terminal)))
        }
    }

    // MARK: - Agents

    func listAgents(_ input: Operations.ListAgents.Input) async throws
        -> Operations.ListAgents.Output {
        let payload = mutate { model -> [Agent] in
            model.listAgentsCount += 1
            return model.agents
        }
        try await gate()
        return .ok(.init(body: .json(payload)))
    }

    func getAgent(_ input: Operations.GetAgent.Input) async throws
        -> Operations.GetAgent.Output {
        try await gate()
        let id = input.path.agentId
        guard let agent = agents.first(where: { $0.id.rawValue == id }) else {
            return .notFound(.init(body: .json(.init(
                _tag: .agentNotFound,
                agentId: id,
                message: "No agent \(id)"
            ))))
        }
        return .ok(.init(body: .json(agent)))
    }

    // MARK: - Filesystem

    func checkDirectory(_ input: Operations.CheckDirectory.Input) async throws
        -> Operations.CheckDirectory.Output {
        try await gate()
        let path = input.query.path
        let response = directoryCheck
            ?? Components.Schemas.FsCheckResponse(path: path, state: .available, checkedAt: .now)
        return .ok(.init(body: .json(response)))
    }

    // MARK: - Unimplemented

    func getHealth(_ input: Operations.GetHealth.Input) async throws
        -> Operations.GetHealth.Output {
        throw StubUnimplemented("getHealth")
    }

    func getVersion(_ input: Operations.GetVersion.Input) async throws
        -> Operations.GetVersion.Output {
        throw StubUnimplemented("getVersion")
    }

    /// The app reaches the event stream through `ResourceEventStream`, never
    /// the contract client; `ScriptedEventStream` is the seam tests drive.
    func subscribeEvents(_ input: Operations.SubscribeEvents.Input) async throws
        -> Operations.SubscribeEvents.Output {
        throw StubUnimplemented("subscribeEvents")
    }

    // MARK: - Private

    private func gate() async throws {
        if let delay {
            try? await Task.sleep(for: delay)
        }
        if shouldFail {
            throw ScriptedFailure()
        }
    }

    private func mutate<T>(_ body: (inout State) throws -> T) rethrows -> T {
        try lock.withLock { try body(&state) }
    }
}

// MARK: - Error payloads

private nonisolated func projectNotFound(_ id: String) -> Components.Schemas.ProjectNotFoundJsonEncoding {
    .init(_tag: .projectNotFound, projectId: id, message: "No project \(id)")
}

private nonisolated func threadNotFound(_ id: String) -> Components.Schemas.ThreadNotFoundJsonEncoding {
    .init(_tag: .threadNotFound, threadId: id, message: "No thread \(id)")
}

private nonisolated func terminalNotFound(_ id: String) -> Components.Schemas.TerminalNotFoundJsonEncoding {
    .init(_tag: .terminalNotFound, terminalId: id, message: "No terminal \(id)")
}

/// The failure `shouldFail` raises. Deliberately opaque: the stores only ever
/// surface `localizedDescription`.
struct ScriptedFailure: LocalizedError {
    var errorDescription: String? { "scripted failure" }
}

/// Raised by operations the app never calls, so a future caller fails loudly
/// instead of reading a fabricated response.
struct StubUnimplemented: LocalizedError {
    let operation: String

    init(_ operation: String) {
        self.operation = operation
    }

    var errorDescription: String? { "\(operation) is not implemented by the test client" }
}

// MARK: - Fixtures

/// Domain fixtures with contract-complete defaults, so a test names only the
/// fields it is actually asserting on.
enum Fixtures {
    static let epoch = Date(timeIntervalSince1970: 1_700_000_000)

    /// `epoch + seconds` — readable, ordered creation timestamps.
    static func date(_ seconds: TimeInterval) -> Date {
        epoch.addingTimeInterval(seconds)
    }

    static func project(
        id: String,
        name: String? = nil,
        workingDirectory: String = "/home/dev/app",
        createdAt: Date = epoch
    ) -> Project {
        Project(
            id: id,
            name: name ?? id,
            defaultWorkingDirectory: workingDirectory,
            createdAt: createdAt,
            updatedAt: createdAt
        )
    }

    static func thread(
        id: String,
        projectId: String = "prj",
        agentId: AgentID = .codex,
        name: String? = nil,
        workingDirectory: String = "/home/dev/app",
        activityState: ThreadActivityState = .idle,
        linkedTerminalId: String? = nil,
        pinnedAt: Date? = nil,
        archivedAt: Date? = nil,
        createdAt: Date = epoch
    ) -> ATCThread {
        ATCThread(
            id: id,
            projectId: projectId,
            agentId: agentId,
            name: name,
            workingDirectory: workingDirectory,
            activityState: activityState,
            linkedTerminalId: linkedTerminalId,
            pinnedAt: pinnedAt,
            archivedAt: archivedAt,
            createdAt: createdAt,
            updatedAt: createdAt
        )
    }

    static func terminal(
        id: String,
        projectId: String = "prj",
        threadId: String? = nil,
        name: String? = nil,
        command: [String]? = nil,
        workingDirectory: String = "/home/dev/app",
        status: TerminalStatus = .live,
        createdAt: Date = epoch
    ) -> Terminal {
        Terminal(
            id: id,
            projectId: projectId,
            threadId: threadId,
            name: name,
            command: command,
            initialWorkingDirectory: workingDirectory,
            status: status,
            createdAt: createdAt,
            updatedAt: createdAt,
            endedAt: status == .ended ? createdAt : nil
        )
    }

    static func agent(_ id: AgentID, available: Bool = true) -> Agent {
        Agent(
            id: id,
            available: available,
            reason: available ? nil : "Not installed",
            detectedVersion: available ? "1.0.0" : nil,
            testedVersion: "1.0.0",
            tuiObservation: id == .codex ? .sharedServer : .hooks
        )
    }

    /// One project, two agents, three active threads (`thr1` oldest), one
    /// archived thread, one standalone live terminal, and one ended terminal.
    static func seed(_ client: ScriptableAppServerClient) {
        client.projects = [project(id: "prj", name: "App", createdAt: date(0))]
        client.agents = [agent(.codex), agent(.claudeCode)]
        client.threads = [
            thread(id: "thr1", createdAt: date(10)),
            thread(id: "thr2", createdAt: date(20)),
            thread(id: "thr3", createdAt: date(30)),
            thread(id: "thr_archived", archivedAt: date(40), createdAt: date(5)),
        ]
        client.terminals = [
            terminal(id: "trm_live", createdAt: date(50)),
            terminal(id: "trm_ended", status: .ended, createdAt: date(60)),
        ]
    }
}

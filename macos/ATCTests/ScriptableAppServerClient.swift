import ATCAppServerAPI
import ATCChat
import Foundation
import OpenAPIRuntime

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
        /// Model catalogs served by listAgentModels, by agent.
        var models: [AgentID: [AgentModel]] = [:]
        /// updateThread settings patches received, in order.
        var settingsPatches: [(id: String, patch: ThreadSettingsPatch)] = []
        var shouldFail = false
        var delay: Duration?
        var directoryCheck: Components.Schemas.FsCheckResponse?
        var directoryListings: [String?: Components.Schemas.FsListResponse] = [:]
        var directoryListingFailure: Components.Schemas.DirectoryUnavailableJsonEncoding?
        /// While set, openThreadTerminal fails 503 with this payload — the
        /// install-or-configure message the unwrap seam must surface.
        var openThreadTerminalFailure: Components.Schemas.ProviderUnavailableJsonEncoding?
        /// While set, openThreadTerminal is refused 409 ThreadBusy (the
        /// server is driving a turn and launches the TUI itself later).
        var openThreadTerminalBusy = false
        var closeThreadTerminalCount = 0
        /// The Thread runtime (ATC-193) as seen by one client: a transcript
        /// page per thread (an unseeded thread reads as empty), the pending
        /// requests and queue, and what clients did about them.
        var transcripts: [String: ThreadTranscriptPage] = [:]
        var threadRequests: [String: [ThreadRequest]] = [:]
        var threadQueues: [String: [QueuedPrompt]] = [:]
        /// While set, promptThread fails 503 with this payload.
        var promptThreadFailure: Components.Schemas.ProviderUnavailableJsonEncoding?
        var transcriptReads: [(threadID: String, before: String?)] = []
        var prompts: [(threadID: String, prompt: String, attachments: [String]?, when: PromptWhen?)] = []
        /// What searchFiles answers with (filtered by the query) and the queries asked.
        var fileSearchEntries: [Components.Schemas.FsFileEntry] = []
        var fileSearches: [(dir: String, query: String?)] = []
        /// Command lists by agent, and the (agent, dir) pairs asked for.
        var agentCommands: [AgentID: [Components.Schemas.AgentCommand]] = [:]
        var commandListings: [(agentID: String, dir: String)] = []
        /// Uploads received, in order, with the bytes as sent.
        var uploads: [(threadID: String, attachment: Components.Schemas.ThreadAttachment, bytes: Data)] = []
        var answers: [(threadID: String, requestID: String, answer: ThreadRequestAnswer)] = []
        var withdrawnPrompts: [(threadID: String, promptID: String)] = []
        var interruptCount = 0
        var createdThreadRequests: [Components.Schemas.CreateThreadRequest] = []
        var createdTerminalRequests: [Components.Schemas.CreateTerminalRequest] = []
        var listProjectsCount = 0
        var listThreadsCount = 0
        var listTerminalsCount = 0
        var listAgentsCount = 0
        var openThreadTerminalCount = 0
        var markThreadViewedCount = 0
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
            let id = nextID("trm")
            return Terminal(
                id: id,
                projectId: projectId,
                threadId: threadId,
                name: name,
                command: command,
                initialWorkingDirectory: workingDirectory,
                status: .live,
                sessionName: String(format: "atc-%032x", idCounter),
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

    var models: [AgentID: [AgentModel]] {
        get { lock.withLock { state.models } }
        set { lock.withLock { state.models = newValue } }
    }

    var settingsPatches: [(id: String, patch: ThreadSettingsPatch)] {
        lock.withLock { state.settingsPatches }
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

    /// Listings served by `listDirectory`, keyed by the requested path
    /// (`nil` = the home-directory request). A request for an unseeded path
    /// throws, standing in for a transport failure.
    var directoryListings: [String?: Components.Schemas.FsListResponse] {
        get { lock.withLock { state.directoryListings } }
        set { lock.withLock { state.directoryListings = newValue } }
    }

    /// While set, every `listDirectory` answers the documented 422 with this
    /// payload — the server's tagged DirectoryUnavailable diagnostic.
    var directoryListingFailure: Components.Schemas.DirectoryUnavailableJsonEncoding? {
        get { lock.withLock { state.directoryListingFailure } }
        set { lock.withLock { state.directoryListingFailure = newValue } }
    }

    var openThreadTerminalFailure: Components.Schemas.ProviderUnavailableJsonEncoding? {
        get { lock.withLock { state.openThreadTerminalFailure } }
        set { lock.withLock { state.openThreadTerminalFailure = newValue } }
    }

    var openThreadTerminalBusy: Bool {
        get { lock.withLock { state.openThreadTerminalBusy } }
        set { lock.withLock { state.openThreadTerminalBusy = newValue } }
    }

    var closeThreadTerminalCount: Int { lock.withLock { state.closeThreadTerminalCount } }

    /// Transcript page served for a thread id; unseeded threads read empty.
    var transcripts: [String: ThreadTranscriptPage] {
        get { lock.withLock { state.transcripts } }
        set { lock.withLock { state.transcripts = newValue } }
    }

    var threadRequests: [String: [ThreadRequest]] {
        get { lock.withLock { state.threadRequests } }
        set { lock.withLock { state.threadRequests = newValue } }
    }

    var threadQueues: [String: [QueuedPrompt]] {
        get { lock.withLock { state.threadQueues } }
        set { lock.withLock { state.threadQueues = newValue } }
    }

    var promptThreadFailure: Components.Schemas.ProviderUnavailableJsonEncoding? {
        get { lock.withLock { state.promptThreadFailure } }
        set { lock.withLock { state.promptThreadFailure = newValue } }
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
    var markThreadViewedCount: Int { lock.withLock { state.markThreadViewedCount } }
    var transcriptReads: [(threadID: String, before: String?)] { lock.withLock { state.transcriptReads } }
    var prompts: [(threadID: String, prompt: String, attachments: [String]?, when: PromptWhen?)] {
        lock.withLock { state.prompts }
    }
    var fileSearchEntries: [Components.Schemas.FsFileEntry] {
        get { lock.withLock { state.fileSearchEntries } }
        set { lock.withLock { state.fileSearchEntries = newValue } }
    }
    var fileSearches: [(dir: String, query: String?)] { lock.withLock { state.fileSearches } }
    var agentCommands: [AgentID: [Components.Schemas.AgentCommand]] {
        get { lock.withLock { state.agentCommands } }
        set { lock.withLock { state.agentCommands = newValue } }
    }
    var commandListings: [(agentID: String, dir: String)] { lock.withLock { state.commandListings } }
    var uploads: [(threadID: String, attachment: Components.Schemas.ThreadAttachment, bytes: Data)] {
        lock.withLock { state.uploads }
    }
    var answers: [(threadID: String, requestID: String, answer: ThreadRequestAnswer)] {
        lock.withLock { state.answers }
    }
    var withdrawnPrompts: [(threadID: String, promptID: String)] { lock.withLock { state.withdrawnPrompts } }
    var interruptCount: Int { lock.withLock { state.interruptCount } }

    // MARK: - Projects

    func listProjects(_ input: Operations.ListProjects.Input) async throws
        -> Operations.ListProjects.Output
    {
        let payload = mutate { model -> [Project] in
            model.listProjectsCount += 1
            return model.projects.sorted { $0.createdAt > $1.createdAt }
        }
        try await gate()
        return .ok(.init(body: .json(payload)))
    }

    func createProject(_ input: Operations.CreateProject.Input) async throws
        -> Operations.CreateProject.Output
    {
        guard case .json(let request) = input.body else { throw StubUnimplemented("createProject") }
        try await gate()
        return .ok(
            .init(
                body: .json(
                    mutate { model -> Project in
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
        -> Operations.GetProject.Output
    {
        try await gate()
        let id = input.path.projectId
        guard let project = projects.first(where: { $0.id == id }) else {
            return .notFound(.init(body: .json(projectNotFound(id))))
        }
        return .ok(.init(body: .json(project)))
    }

    func updateProject(_ input: Operations.UpdateProject.Input) async throws
        -> Operations.UpdateProject.Output
    {
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
        -> Operations.DeleteProject.Output
    {
        try await gate()
        let id = input.path.projectId
        return mutate { model -> Operations.DeleteProject.Output in
            guard model.projects.contains(where: { $0.id == id }) else {
                return .notFound(.init(body: .json(projectNotFound(id))))
            }
            // Deletion cascades server-side (ATC-154): owned threads and
            // terminals go with the project.
            model.projects.removeAll { $0.id == id }
            model.threads.removeAll { $0.projectId == id }
            model.terminals.removeAll { $0.projectId == id }
            return .noContent(.init())
        }
    }

    // MARK: - Terminals

    func listTerminals(_ input: Operations.ListTerminals.Input) async throws
        -> Operations.ListTerminals.Output
    {
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
        -> Operations.CreateTerminal.Output
    {
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
        -> Operations.GetTerminal.Output
    {
        try await gate()
        let id = input.path.terminalId
        guard let terminal = terminals.first(where: { $0.id == id }) else {
            return .notFound(.init(body: .json(terminalNotFound(id))))
        }
        return .ok(.init(body: .json(terminal)))
    }

    func updateTerminal(_ input: Operations.UpdateTerminal.Input) async throws
        -> Operations.UpdateTerminal.Output
    {
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
        -> Operations.DeleteTerminal.Output
    {
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
        -> Operations.ListThreads.Output
    {
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
        -> Operations.CreateThread.Output
    {
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
                settings: model.agents.first { $0.id == request.agentId }?.defaults ?? Fixtures.settings(),
                activityState: .idle,
                unread: false,
                createdAt: now,
                updatedAt: now
            )
            model.threads.append(thread)
            return .ok(.init(body: .json(thread)))
        }
    }

    func getThread(_ input: Operations.GetThread.Input) async throws
        -> Operations.GetThread.Output
    {
        try await gate()
        let id = input.path.threadId
        guard let thread = threads.first(where: { $0.id == id }) else {
            return .notFound(.init(body: .json(threadNotFound(id))))
        }
        return .ok(.init(body: .json(thread)))
    }

    func updateThread(_ input: Operations.UpdateThread.Input) async throws
        -> Operations.UpdateThread.Output
    {
        guard case .json(let request) = input.body else { throw StubUnimplemented("updateThread") }
        let id = input.path.threadId
        // Applied before the gate: a delayed response is one the server
        // already committed, so two overlapping patches can complete out
        // of order the way they do over a real connection.
        let output = mutate { model -> Operations.UpdateThread.Output in
            guard let index = model.threads.firstIndex(where: { $0.id == id }) else {
                return .notFound(.init(body: .json(threadNotFound(id))))
            }
            if let name = request.name { model.threads[index].name = name }
            if let patch = request.settings {
                // The server's per-model reasoning rule, as the preview
                // client mirrors it (ThreadSettings.applying).
                let catalog = model.models[model.threads[index].agentId] ?? []
                let settings = model.threads[index].settings.applying(patch, catalog: catalog)
                model.threads[index].settings = settings
                model.settingsPatches.append((id, patch))
                // Like the server: the change writes through to the agent's defaults.
                if let agent = model.agents.firstIndex(where: { $0.id == model.threads[index].agentId }) {
                    model.agents[agent].defaults = settings
                }
            }
            model.threads[index].updatedAt = model.tick()
            return .ok(.init(body: .json(model.threads[index])))
        }
        try await gate()
        return output
    }

    func deleteThread(_ input: Operations.DeleteThread.Input) async throws
        -> Operations.DeleteThread.Output
    {
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
        -> Operations.ArchiveThread.Output
    {
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
        -> Operations.UnarchiveThread.Output
    {
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
        -> Operations.PinThread.Output
    {
        try await gate()
        let id = input.path.threadId
        return mutate { model -> Operations.PinThread.Output in
            guard let index = model.threads.firstIndex(where: { $0.id == id }) else {
                return .notFound(.init(body: .json(threadNotFound(id))))
            }
            guard model.threads[index].archivedAt == nil else {
                return .conflict(
                    .init(
                        body: .json(
                            .init(
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

    /// Idempotent per the contract: only an unread thread is written (and
    /// timestamped); a read one comes back unchanged.
    func markThreadViewed(_ input: Operations.MarkThreadViewed.Input) async throws
        -> Operations.MarkThreadViewed.Output
    {
        try await gate()
        let id = input.path.threadId
        return mutate { model -> Operations.MarkThreadViewed.Output in
            model.markThreadViewedCount += 1
            guard let index = model.threads.firstIndex(where: { $0.id == id }) else {
                return .notFound(.init(body: .json(threadNotFound(id))))
            }
            if model.threads[index].unread {
                model.threads[index].unread = false
                model.threads[index].updatedAt = model.tick()
            }
            return .ok(.init(body: .json(model.threads[index])))
        }
    }

    func unpinThread(_ input: Operations.UnpinThread.Input) async throws
        -> Operations.UnpinThread.Output
    {
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
        -> Operations.OpenThreadTerminal.Output
    {
        try await gate()
        if let failure = openThreadTerminalFailure {
            return .serviceUnavailable(.init(body: .json(.init(value1: failure))))
        }
        let id = input.path.threadId
        return mutate { model -> Operations.OpenThreadTerminal.Output in
            model.openThreadTerminalCount += 1
            guard let index = model.threads.firstIndex(where: { $0.id == id }) else {
                return .notFound(.init(body: .json(threadNotFound(id))))
            }
            if model.openThreadTerminalBusy {
                return .conflict(
                    .init(
                        body: .json(
                            .init(
                                value2: .init(
                                    _tag: .threadBusy, threadId: id,
                                    message: "thread \(id) is working; retry once the turn completes")))))
            }
            let thread = model.threads[index]
            if let linkedID = thread.linkedTerminalId,
                let existing = model.terminals.first(where: {
                    $0.id == linkedID && $0.status == .live
                })
            {
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

    /// The Claude hand-off as one client sees it: the linked terminal ends
    /// (its record removed, the thread unlinked). A Codex TUI would keep
    /// running; this double models the one-process provider.
    func closeThreadTerminal(_ input: Operations.CloseThreadTerminal.Input) async throws
        -> Operations.CloseThreadTerminal.Output
    {
        try await gate()
        let id = input.path.threadId
        return mutate { model -> Operations.CloseThreadTerminal.Output in
            model.closeThreadTerminalCount += 1
            guard let index = model.threads.firstIndex(where: { $0.id == id }) else {
                return .notFound(.init(body: .json(threadNotFound(id))))
            }
            if let linkedID = model.threads[index].linkedTerminalId {
                model.terminals.removeAll { $0.id == linkedID }
                model.threads[index].linkedTerminalId = nil
                model.threads[index].updatedAt = model.tick()
            }
            return .ok(.init(body: .json(model.threads[index])))
        }
    }

    // MARK: - Agents

    func listAgents(_ input: Operations.ListAgents.Input) async throws
        -> Operations.ListAgents.Output
    {
        let payload = mutate { model -> [Agent] in
            model.listAgentsCount += 1
            return model.agents
        }
        try await gate()
        return .ok(.init(body: .json(payload)))
    }

    func searchFiles(_ input: Operations.SearchFiles.Input) async throws -> Operations.SearchFiles.Output {
        try await gate()
        let dir = input.query.dir
        let needle = (input.query.query ?? "").lowercased()
        return mutate { model -> Operations.SearchFiles.Output in
            model.fileSearches.append((dir, input.query.query))
            let entries = model.fileSearchEntries.filter { needle.isEmpty || $0.path.lowercased().contains(needle) }
            return .ok(.init(body: .json(.init(dir: dir, entries: entries, truncated: false))))
        }
    }

    func listAgentCommands(_ input: Operations.ListAgentCommands.Input) async throws
        -> Operations.ListAgentCommands.Output
    {
        try await gate()
        let id = input.path.agentId
        guard let agentId = AgentID(rawValue: id) else {
            return .notFound(
                .init(body: .json(.init(_tag: .agentNotFound, agentId: id, message: "No agent \(id)"))))
        }
        return mutate { model -> Operations.ListAgentCommands.Output in
            model.commandListings.append((id, input.query.dir))
            return .ok(.init(body: .json(model.agentCommands[agentId] ?? [])))
        }
    }

    func listAgentModels(_ input: Operations.ListAgentModels.Input) async throws
        -> Operations.ListAgentModels.Output
    {
        try await gate()
        let id = input.path.agentId
        guard let agentId = AgentID(rawValue: id) else {
            return .notFound(
                .init(body: .json(.init(_tag: .agentNotFound, agentId: id, message: "No agent \(id)"))))
        }
        return .ok(.init(body: .json(mutate { model in model.models[agentId] ?? [] })))
    }

    func getAgent(_ input: Operations.GetAgent.Input) async throws
        -> Operations.GetAgent.Output
    {
        try await gate()
        let id = input.path.agentId
        guard let agent = agents.first(where: { $0.id.rawValue == id }) else {
            return .notFound(
                .init(
                    body: .json(
                        .init(
                            _tag: .agentNotFound,
                            agentId: id,
                            message: "No agent \(id)"
                        ))))
        }
        return .ok(.init(body: .json(agent)))
    }

    // MARK: - Filesystem

    func checkDirectory(_ input: Operations.CheckDirectory.Input) async throws
        -> Operations.CheckDirectory.Output
    {
        try await gate()
        let path = input.query.path
        let response =
            directoryCheck
            ?? Components.Schemas.FsCheckResponse(path: path, state: .available, checkedAt: .now)
        return .ok(.init(body: .json(response)))
    }

    func listDirectory(_ input: Operations.ListDirectory.Input) async throws
        -> Operations.ListDirectory.Output
    {
        try await gate()
        if let failure = directoryListingFailure {
            return .unprocessableContent(.init(body: .json(.init(value1: failure))))
        }
        guard let listing = directoryListings[input.query.path] else {
            throw StubUnimplemented("listDirectory \(input.query.path ?? "<home>")")
        }
        return .ok(.init(body: .json(listing)))
    }

    // MARK: - Unimplemented

    func getHealth(_ input: Operations.GetHealth.Input) async throws
        -> Operations.GetHealth.Output
    {
        throw StubUnimplemented("getHealth")
    }

    func getVersion(_ input: Operations.GetVersion.Input) async throws
        -> Operations.GetVersion.Output
    {
        throw StubUnimplemented("getVersion")
    }

    func getServerInfo(_ input: Operations.GetServerInfo.Input) async throws
        -> Operations.GetServerInfo.Output
    {
        throw StubUnimplemented("getServerInfo")
    }

    /// The app reaches the event stream through `ResourceEventStream`, never
    /// the contract client; `ScriptedEventStream` is the seam tests drive.
    func subscribeEvents(_ input: Operations.SubscribeEvents.Input) async throws
        -> Operations.SubscribeEvents.Output
    {
        throw StubUnimplemented("subscribeEvents")
    }

    // MARK: - Thread runtime (ATC-193)

    /// Always admitted, always starting a turn at once (this stand-in never
    /// models a busy thread — queue state is seeded directly).
    func promptThread(_ input: Operations.PromptThread.Input) async throws
        -> Operations.PromptThread.Output
    {
        guard case .json(let request) = input.body else { throw StubUnimplemented("promptThread") }
        try await gate()
        if let failure = promptThreadFailure {
            return .serviceUnavailable(.init(body: .json(failure)))
        }
        let id = input.path.threadId
        return mutate { model -> Operations.PromptThread.Output in
            guard model.threads.contains(where: { $0.id == id }) else {
                return .notFound(.init(body: .json(.init(value1: threadNotFound(id)))))
            }
            model.prompts.append((id, request.prompt, request.attachments, request.when))
            return .ok(.init(body: .json(.init(promptId: model.nextID("prm"), turnId: model.nextID("turn")))))
        }
    }

    func createThreadAttachment(_ input: Operations.CreateThreadAttachment.Input) async throws
        -> Operations.CreateThreadAttachment.Output
    {
        let (mediaType, body): (Components.Schemas.AttachmentMediaType, HTTPBody) =
            switch input.body {
            case .png(let body): (.imagePng, body)
            case .jpeg(let body): (.imageJpeg, body)
            case .imageGif(let body): (.imageGif, body)
            case .imageWebp(let body): (.imageWebp, body)
            }
        // Collect just past the server's cap so an oversized body is the
        // documented 413, never a stored upload.
        let cap = ImageAttachmentEncoder.maxBytes
        let bytes = try await Data(collecting: body, upTo: cap + 1)
        try await gate()
        let id = input.path.threadId
        return mutate { model -> Operations.CreateThreadAttachment.Output in
            guard model.threads.contains(where: { $0.id == id }) else {
                return .notFound(.init(body: .json(threadNotFound(id))))
            }
            guard bytes.count <= cap else {
                return .contentTooLarge(
                    .init(
                        body: .json(
                            .init(
                                _tag: .attachmentTooLarge, threadId: id, byteSize: bytes.count, limit: cap,
                                message: "attachment of \(bytes.count) bytes exceeds the \(cap)-byte limit"))))
            }
            let attachmentID = model.nextID("att")
            let attachment = Components.Schemas.ThreadAttachment(
                id: attachmentID, name: input.query.name ?? "image", mediaType: mediaType,
                byteSize: bytes.count, path: "/data/attachments/\(id)/\(attachmentID)", createdAt: model.tick())
            model.uploads.append((id, attachment, bytes))
            return .ok(.init(body: .json(attachment)))
        }
    }

    func getThreadAttachment(_ input: Operations.GetThreadAttachment.Input) async throws
        -> Operations.GetThreadAttachment.Output
    {
        try await gate()
        let upload = mutate { model in
            model.uploads.first {
                $0.threadID == input.path.threadId && $0.attachment.id == input.path.attachmentId
            }
        }
        guard let upload else {
            return .notFound(
                .init(
                    body: .json(
                        .init(
                            value2: .init(
                                _tag: .attachmentNotFound, threadId: input.path.threadId,
                                attachmentId: input.path.attachmentId, message: "No attachment")))))
        }
        return .ok(.init(body: .binary(HTTPBody(upload.bytes))))
    }

    /// Serves the seeded page for the thread; a `before` cursor reads as an
    /// empty page (the contract's "cursor from a replaced copy" case) unless
    /// the test seeded a page under `"<threadId>@before=<cursor>"`.
    func getThreadTranscript(_ input: Operations.GetThreadTranscript.Input) async throws
        -> Operations.GetThreadTranscript.Output
    {
        let id = input.path.threadId
        let before = input.query.before
        let page = mutate { model -> ThreadTranscriptPage? in
            model.transcriptReads.append((id, before))
            guard model.threads.contains(where: { $0.id == id }) else { return nil }
            if let before {
                return model.transcripts["\(id)@before=\(before)"]
                    ?? ThreadTranscriptPage(items: [], turns: [], seq: 0, snapshotVersion: 0, hasMore: false)
            }
            return model.transcripts[id]
                ?? ThreadTranscriptPage(items: [], turns: [], seq: 0, snapshotVersion: 0, hasMore: false)
        }
        try await gate()
        guard let page else { return .notFound(.init(body: .json(threadNotFound(id)))) }
        return .ok(.init(body: .json(page)))
    }

    /// The app reaches the per-thread stream through `ThreadEventStream`,
    /// never the contract client; `ScriptedThreadEventStream` is the seam.
    func subscribeThreadEvents(_ input: Operations.SubscribeThreadEvents.Input) async throws
        -> Operations.SubscribeThreadEvents.Output
    {
        throw StubUnimplemented("subscribeThreadEvents")
    }

    func interruptThread(_ input: Operations.InterruptThread.Input) async throws
        -> Operations.InterruptThread.Output
    {
        try await gate()
        let id = input.path.threadId
        return mutate { model -> Operations.InterruptThread.Output in
            guard model.threads.contains(where: { $0.id == id }) else {
                return .notFound(.init(body: .json(threadNotFound(id))))
            }
            model.interruptCount += 1
            return .noContent(.init())
        }
    }

    func listThreadRequests(_ input: Operations.ListThreadRequests.Input) async throws
        -> Operations.ListThreadRequests.Output
    {
        let id = input.path.threadId
        let requests = mutate { model -> [ThreadRequest]? in
            model.threads.contains(where: { $0.id == id }) ? model.threadRequests[id] ?? [] : nil
        }
        try await gate()
        guard let requests else { return .notFound(.init(body: .json(threadNotFound(id)))) }
        return .ok(.init(body: .json(requests)))
    }

    /// Records the answer and closes the request (a live server would also
    /// emit `request.closed`; tests push that through the scripted stream).
    func answerThreadRequest(_ input: Operations.AnswerThreadRequest.Input) async throws
        -> Operations.AnswerThreadRequest.Output
    {
        guard case .json(let answer) = input.body else { throw StubUnimplemented("answerThreadRequest") }
        try await gate()
        let id = input.path.threadId
        let requestID = input.path.requestId
        return mutate { model -> Operations.AnswerThreadRequest.Output in
            guard model.threads.contains(where: { $0.id == id }) else {
                return .notFound(.init(body: .json(.init(value1: threadNotFound(id)))))
            }
            guard model.threadRequests[id, default: []].contains(where: { $0.id == requestID }) else {
                return .notFound(
                    .init(
                        body: .json(
                            .init(
                                value2: .init(
                                    _tag: .requestNotFound, threadId: id, requestId: requestID,
                                    message: "No pending request \(requestID)")))))
            }
            model.answers.append((id, requestID, answer))
            model.threadRequests[id]?.removeAll { $0.id == requestID }
            return .noContent(.init())
        }
    }

    func listThreadQueue(_ input: Operations.ListThreadQueue.Input) async throws
        -> Operations.ListThreadQueue.Output
    {
        let id = input.path.threadId
        let queue = mutate { model -> [QueuedPrompt]? in
            model.threads.contains(where: { $0.id == id }) ? model.threadQueues[id] ?? [] : nil
        }
        try await gate()
        guard let queue else { return .notFound(.init(body: .json(threadNotFound(id)))) }
        return .ok(.init(body: .json(queue)))
    }

    func deleteQueuedPrompt(_ input: Operations.DeleteQueuedPrompt.Input) async throws
        -> Operations.DeleteQueuedPrompt.Output
    {
        try await gate()
        let id = input.path.threadId
        let promptID = input.path.promptId
        return mutate { model -> Operations.DeleteQueuedPrompt.Output in
            guard model.threads.contains(where: { $0.id == id }) else {
                return .notFound(.init(body: .json(.init(value1: threadNotFound(id)))))
            }
            guard model.threadQueues[id, default: []].contains(where: { $0.id == promptID }) else {
                return .notFound(
                    .init(
                        body: .json(
                            .init(
                                value2: .init(
                                    _tag: .queuedPromptNotFound, threadId: id, promptId: promptID,
                                    message: "No queued prompt \(promptID)")))))
            }
            model.withdrawnPrompts.append((id, promptID))
            model.threadQueues[id]?.removeAll { $0.id == promptID }
            return .noContent(.init())
        }
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
        settings: ThreadSettings = Fixtures.settings(),
        activityState: ThreadActivityState = .idle,
        unread: Bool = false,
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
            settings: settings,
            activityState: activityState,
            unread: unread,
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
        sessionName: String = "atc-00000000000000000000000000000000",
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
            sessionName: sessionName,
            createdAt: createdAt,
            updatedAt: createdAt,
            endedAt: status == .ended ? createdAt : nil
        )
    }

    // MARK: Thread runtime

    static func userMessage(_ id: String, turn: String = "turn1", text: String = "hi", parent: String? = nil)
        -> ThreadItem
    {
        .userMessage(.init(_type: .userMessage, id: id, turnId: turn, parentItemId: parent, text: text))
    }

    static func assistantText(
        _ id: String, turn: String = "turn1", text: String = "", complete: Bool = false, parent: String? = nil
    ) -> ThreadItem {
        .assistantText(
            .init(_type: .assistantText, id: id, turnId: turn, parentItemId: parent, text: text, complete: complete))
    }

    static func command(
        _ id: String, turn: String = "turn1", title: String = "bun test", status: ToolStatus = .running,
        parent: String? = nil, output: String? = nil, exitCode: Int? = nil
    ) -> ThreadItem {
        .command(
            .init(
                _type: .command, id: id, turnId: turn, parentItemId: parent, title: title, status: status,
                command: title, output: output, exitCode: exitCode))
    }

    static func turn(
        _ id: String, status: Components.Schemas.ThreadTurnStatus = .running, error: String? = nil,
        promptId: String? = nil
    ) -> ThreadTurn {
        ThreadTurn(id: id, status: status, error: error, promptId: promptId)
    }

    static func page(
        items: [ThreadItem], turns: [ThreadTurn] = [], seq: Int, snapshotVersion: Int = 1, hasMore: Bool = false
    ) -> ThreadTranscriptPage {
        ThreadTranscriptPage(items: items, turns: turns, seq: seq, snapshotVersion: snapshotVersion, hasMore: hasMore)
    }

    static func queued(_ id: String, prompt: String = "later") -> QueuedPrompt {
        QueuedPrompt(id: id, prompt: prompt, queuedAt: epoch)
    }

    static func approval(_ id: String, turn: String = "turn1", title: String = "Run bun test?") -> ThreadRequest {
        .approval(
            .init(
                kind: .approval, id: id, turnId: turn, openedAt: epoch, title: title,
                subject: .command(.init(_type: .command, command: "bun test"))))
    }

    static func agent(_ id: AgentID, available: Bool = true) -> Agent {
        Agent(
            id: id,
            available: available,
            reason: available ? nil : "Not installed",
            detectedVersion: available ? "1.0.0" : nil,
            defaults: settings()
        )
    }

    nonisolated static func settings(
        model: String = "test-model",
        reasoning: ReasoningLevel? = .high,
        mode: ThreadMode = .chat,
        access: ThreadAccess = .auto
    ) -> ThreadSettings {
        ThreadSettings(model: model, reasoning: reasoning, mode: mode, access: access)
    }

    static func model(
        _ value: String,
        displayName: String? = nil,
        isDefault: Bool = false,
        effortLevels: [ReasoningLevel] = [.low, .medium, .high]
    ) -> AgentModel {
        AgentModel(
            value: value,
            displayName: displayName ?? value,
            description: "",
            isDefault: isDefault,
            supportedEffortLevels: effortLevels,
            defaultEffortLevel: effortLevels.contains(.medium) ? .medium : effortLevels.first
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

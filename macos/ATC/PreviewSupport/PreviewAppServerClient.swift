// Canned App Server for previews and offline development: one Connection's
// worth of projects, threads, terminals, and agents.
//
// It is a value type, so mutations echo a plausible response but do not
// persist — the next refresh returns the fixtures again. That is the same
// fidelity limit the old mock had, and it is deliberate: previews exercise
// presentation, not server state.

import ATCAppServerAPI
import ATCAppServerTransport
import Foundation
import OpenAPIRuntime

// Fixtures only: previews and tests. Never compiled into a Release build.
#if DEBUG

    extension ThreadSettings {
        /// The server's settings rule, mirrored for the stand-ins (this
        /// client and the tests' scriptable one) so what they echo is a
        /// contract-valid `ThreadSettings`: on a model change, reasoning
        /// carries over when the new model supports it, else the model's
        /// default applies — none for a model without effort support.
        /// Production never applies patches; the server does.
        nonisolated func applying(_ patch: ThreadSettingsPatch, catalog: [AgentModel]) -> ThreadSettings {
            var settings = self
            if let model = patch.model { settings.model = model }
            if let entry = catalog.first(where: { $0.value == settings.model }), patch.model != nil {
                settings.reasoning =
                    settings.reasoning.map { entry.supportedEffortLevels.contains($0) } == true
                    ? settings.reasoning : entry.defaultEffortLevel
            }
            if let reasoning = patch.reasoning { settings.reasoning = reasoning }
            if let mode = patch.mode { settings.mode = mode }
            if let access = patch.access { settings.access = access }
            return settings
        }
    }

    extension AppModel {
        /// Preview/test fixture: an ephemeral Connection store (unique
        /// UserDefaults suite, nothing persisted to `.standard`) holding one
        /// Connection backed by the canned client, with terminal attaches stubbed
        /// out so a preview never opens a socket.
        static func preview(client: any APIProtocol = PreviewAppServerClient()) -> AppModel {
            let defaults = UserDefaults(suiteName: "preview.appmodel.\(UUID().uuidString)")!
            let store = ConnectionsStore(defaults: defaults, credentials: InMemoryCredentialStore())
            _ = try? store.add(name: "Workstation", urlString: "http://127.0.0.1:7331", token: "")
            return AppModel(
                connections: store,
                clientFactory: { _, _ in client },
                terminalControllerFactory: { terminalID, runtime in
                    TerminalSessionController(
                        terminalID: terminalID,
                        endpoint: AttachEndpoint(baseURL: runtime.baseURL, headers: [:]),
                        checkLive: { true },
                        connectionFactory: { _, _ in
                            TerminalAttachHandle(
                                // A stream that never yields keeps the controller
                                // in `.connecting` without any I/O.
                                start: { AsyncStream { _ in } },
                                enqueue: { _ in },
                                enqueueResize: { _, _ in },
                                close: {}
                            )
                        }
                    )
                },
                terminalRecoveryMonitor: .disabled(),
                eventStreamFactory: { _, _ in AsyncStream { $0.yield(.connected) } },
                threadEventStreamFactory: { _, _, _, _ in AsyncStream { $0.yield(.connected) } }
            )
        }
    }

    nonisolated struct PreviewAppServerClient: APIProtocol {
        // MARK: - Fixtures

        static let atelier = Project(
            id: "0192f4a0-0000-7000-8000-000000000001",
            name: "Atelier",
            defaultWorkingDirectory: "/Users/dev/Projects/atelier",
            createdAt: Date(timeIntervalSinceNow: -400_000),
            updatedAt: Date(timeIntervalSinceNow: -60)
        )

        static let blazerr = Project(
            id: "0192f4a0-0000-7000-8000-000000000002",
            name: "Blazerr",
            defaultWorkingDirectory: "/Users/dev/Projects/blazerr",
            createdAt: Date(timeIntervalSinceNow: -300_000),
            updatedAt: Date(timeIntervalSinceNow: -3_600)
        )

        var projects: [Project] { [Self.atelier, Self.blazerr] }

        var threads: [ATCThread] {
            [
                ATCThread(
                    id: "0192f4b0-0000-7000-8000-000000000001",
                    projectId: Self.atelier.id,
                    agentId: .claudeCode,
                    kind: .tui,
                    name: "Parser rewrite",
                    workingDirectory: Self.atelier.defaultWorkingDirectory,
                    settings: Self.claudeSettings,
                    activityState: .working,
                    unread: false,
                    linkedTerminalId: "0192f4c0-0000-7000-8000-000000000001",
                    pinnedAt: Date(timeIntervalSinceNow: -200_000),
                    createdAt: Date(timeIntervalSinceNow: -220_000),
                    updatedAt: Date(timeIntervalSinceNow: -30)
                ),
                ATCThread(
                    id: "0192f4b0-0000-7000-8000-000000000002",
                    projectId: Self.atelier.id,
                    agentId: .codex,
                    kind: .chat,
                    name: "Flaky test triage",
                    workingDirectory: Self.atelier.defaultWorkingDirectory,
                    settings: Self.codexSettings,
                    activityState: .needsInput,
                    unread: false,
                    createdAt: Date(timeIntervalSinceNow: -7_200),
                    updatedAt: Date(timeIntervalSinceNow: -120)
                ),
                ATCThread(
                    id: "0192f4b0-0000-7000-8000-000000000003",
                    projectId: Self.atelier.id,
                    agentId: .codex,
                    kind: .chat,
                    workingDirectory: "/Users/dev/Projects/atelier/packages/core",
                    settings: Self.codexSettings,
                    activityState: .idle,
                    // The Done card: finished while nobody was looking.
                    unread: true,
                    createdAt: Date(timeIntervalSinceNow: -20_000),
                    updatedAt: Date(timeIntervalSinceNow: -19_000)
                ),
                ATCThread(
                    id: "0192f4b0-0000-7000-8000-000000000004",
                    projectId: Self.blazerr.id,
                    agentId: .claudeCode,
                    kind: .chat,
                    name: "Spike: streaming uploads",
                    workingDirectory: Self.blazerr.defaultWorkingDirectory,
                    settings: Self.claudeSettings,
                    activityState: .idle,
                    unread: false,
                    createdAt: Date(timeIntervalSinceNow: -90_000),
                    updatedAt: Date(timeIntervalSinceNow: -86_400)
                ),
                ATCThread(
                    id: "0192f4b0-0000-7000-8000-000000000005",
                    projectId: Self.blazerr.id,
                    agentId: .codex,
                    kind: .chat,
                    name: "Dependency bump",
                    workingDirectory: Self.blazerr.defaultWorkingDirectory,
                    settings: Self.codexSettings,
                    activityState: .unknown,
                    unread: false,
                    createdAt: Date(timeIntervalSinceNow: -150_000),
                    updatedAt: Date(timeIntervalSinceNow: -140_000)
                ),
            ]
        }

        var archivedThreads: [ATCThread] {
            [
                ATCThread(
                    id: "0192f4b0-0000-7000-8000-000000000006",
                    projectId: Self.atelier.id,
                    agentId: .claudeCode,
                    kind: .chat,
                    name: "Abandoned migration",
                    workingDirectory: Self.atelier.defaultWorkingDirectory,
                    settings: Self.claudeSettings,
                    activityState: .unknown,
                    unread: false,
                    archivedAt: Date(timeIntervalSinceNow: -40_000),
                    createdAt: Date(timeIntervalSinceNow: -500_000),
                    updatedAt: Date(timeIntervalSinceNow: -40_000)
                )
            ]
        }

        var terminals: [Terminal] {
            [
                // The pinned thread's TUI terminal — never a sidebar row.
                Terminal(
                    id: "0192f4c0-0000-7000-8000-000000000001",
                    projectId: Self.atelier.id,
                    threadId: "0192f4b0-0000-7000-8000-000000000001",
                    initialWorkingDirectory: Self.atelier.defaultWorkingDirectory,
                    status: .live,
                    sessionName: "atc-0192f4c0000070008000000000000001",
                    createdAt: Date(timeIntervalSinceNow: -220_000),
                    updatedAt: Date(timeIntervalSinceNow: -30)
                ),
                Terminal(
                    id: "0192f4c0-0000-7000-8000-000000000002",
                    projectId: Self.atelier.id,
                    name: "Server logs",
                    initialWorkingDirectory: Self.atelier.defaultWorkingDirectory,
                    status: .live,
                    sessionName: "atc-0192f4c0000070008000000000000002",
                    createdAt: Date(timeIntervalSinceNow: -5_000),
                    updatedAt: Date(timeIntervalSinceNow: -60)
                ),
                Terminal(
                    id: "0192f4c0-0000-7000-8000-000000000003",
                    projectId: Self.atelier.id,
                    command: ["lazygit"],
                    initialWorkingDirectory: Self.atelier.defaultWorkingDirectory,
                    status: .ended,
                    sessionName: "atc-0192f4c0000070008000000000000003",
                    createdAt: Date(timeIntervalSinceNow: -900),
                    updatedAt: Date(timeIntervalSinceNow: -600),
                    endedAt: Date(timeIntervalSinceNow: -600)
                ),
            ]
        }

        var agents: [Agent] {
            [
                Agent(
                    id: .codex,
                    available: true,
                    detectedVersion: "0.52.0",
                    defaults: Self.codexSettings
                ),
                Agent(
                    id: .claudeCode,
                    available: false,
                    reason: "Install the Claude Code CLI",
                    defaults: Self.claudeSettings
                ),
            ]
        }

        static let codexSettings = ThreadSettings(
            model: "gpt-5.6-sol", reasoning: .high, mode: .chat, access: .auto)
        static let claudeSettings = ThreadSettings(
            model: "opus[1m]", reasoning: .high, mode: .chat, access: .auto)

        static let codexModels: [AgentModel] = [
            AgentModel(
                value: "gpt-5.6-sol", displayName: "GPT-5.6-Sol", description: "Latest frontier agentic coding model.",
                isDefault: true,
                supportedEffortLevels: [.low, .medium, .high, .xhigh, .max, .ultra], defaultEffortLevel: .low),
            AgentModel(
                value: "gpt-5.6-luna", displayName: "GPT-5.6-Luna", description: "Fast and affordable.",
                isDefault: false,
                supportedEffortLevels: [.low, .medium, .high, .xhigh, .max], defaultEffortLevel: .medium),
        ]
        static let claudeModels: [AgentModel] = [
            AgentModel(
                value: "opus[1m]", displayName: "Opus 5 (1M context)", description: "Opus 5 with 1M context",
                isDefault: true,
                supportedEffortLevels: [.low, .medium, .high, .xhigh, .max], defaultEffortLevel: .high),
            AgentModel(
                value: "claude-fable-5[1m]", displayName: "Fable 5", description: "Fable 5",
                isDefault: false,
                supportedEffortLevels: [.low, .medium, .high, .xhigh, .max], defaultEffortLevel: .high),
            AgentModel(
                value: "haiku", displayName: "Haiku 4.5", description: "Haiku 4.5",
                isDefault: false, supportedEffortLevels: []),
        ]

        init() {}

        // MARK: - Meta

        func getHealth(_ input: Operations.GetHealth.Input) async throws -> Operations.GetHealth.Output {
            .ok(.init(body: .json(.init(status: .ok))))
        }

        func getServerInfo(
            _ input: Operations.GetServerInfo.Input
        ) async throws -> Operations.GetServerInfo.Output {
            .ok(.init(body: .json(.init(tailscale: .init(state: .disabled)))))
        }

        func getVersion(_ input: Operations.GetVersion.Input) async throws -> Operations.GetVersion.Output {
            .ok(
                .init(
                    body: .json(
                        .init(
                            version: "0.0.0-preview",
                            apiVersion: .v1,
                            commit: "dev",
                            builtAt: "dev"
                        ))))
        }

        func subscribeEvents(
            _ input: Operations.SubscribeEvents.Input
        ) async throws -> Operations.SubscribeEvents.Output {
            .ok(.init(body: .textEventStream(HTTPBody(": connected\n\n"))))
        }

        func checkDirectory(
            _ input: Operations.CheckDirectory.Input
        ) async throws -> Operations.CheckDirectory.Output {
            .ok(
                .init(
                    body: .json(
                        .init(
                            path: input.query.path,
                            state: .available,
                            checkedAt: Date()
                        ))))
        }

        func listDirectory(
            _ input: Operations.ListDirectory.Input
        ) async throws -> Operations.ListDirectory.Output {
            let path = input.query.path ?? "/Users/dev"
            return .ok(
                .init(
                    body: .json(
                        .init(
                            path: path,
                            parent: path == "/"
                                ? nil : URL(filePath: path).deletingLastPathComponent().path(percentEncoded: false),
                            entries: ["Documents", "Downloads", "Projects"].map {
                                .init(name: $0, path: path == "/" ? "/\($0)" : "\(path)/\($0)")
                            }
                        ))))
        }

        // MARK: - Projects

        func listProjects(_ input: Operations.ListProjects.Input) async throws -> Operations.ListProjects.Output {
            .ok(.init(body: .json(projects)))
        }

        func createProject(_ input: Operations.CreateProject.Input) async throws -> Operations.CreateProject.Output {
            guard case .json(let request) = input.body else { throw PreviewUnavailable() }
            return .ok(
                .init(
                    body: .json(
                        Project(
                            id: Self.newID(),
                            name: request.name,
                            defaultWorkingDirectory: request.defaultWorkingDirectory,
                            createdAt: Date(),
                            updatedAt: Date()
                        ))))
        }

        func getProject(_ input: Operations.GetProject.Input) async throws -> Operations.GetProject.Output {
            .ok(.init(body: .json(try require(projects.first { $0.id == input.path.projectId }))))
        }

        func updateProject(_ input: Operations.UpdateProject.Input) async throws -> Operations.UpdateProject.Output {
            guard case .json(let request) = input.body else { throw PreviewUnavailable() }
            var project = try require(projects.first { $0.id == input.path.projectId })
            if let name = request.name { project.name = name }
            if let directory = request.defaultWorkingDirectory {
                project.defaultWorkingDirectory = directory
            }
            project.updatedAt = Date()
            return .ok(.init(body: .json(project)))
        }

        func deleteProject(_ input: Operations.DeleteProject.Input) async throws -> Operations.DeleteProject.Output {
            .noContent(.init())
        }

        // MARK: - Terminals

        func listTerminals(_ input: Operations.ListTerminals.Input) async throws -> Operations.ListTerminals.Output {
            let projectID = input.query.projectId
            return .ok(
                .init(
                    body: .json(
                        terminals.filter { projectID == nil || $0.projectId == projectID }
                    )))
        }

        func createTerminal(_ input: Operations.CreateTerminal.Input) async throws -> Operations.CreateTerminal.Output {
            guard case .json(let request) = input.body else { throw PreviewUnavailable() }
            let project = try require(projects.first { $0.id == request.projectId })
            let id = Self.newID()
            return .ok(
                .init(
                    body: .json(
                        Terminal(
                            id: id,
                            projectId: project.id,
                            name: request.name,
                            command: request.command,
                            initialWorkingDirectory: request.workingDirectory ?? project.defaultWorkingDirectory,
                            status: .live,
                            sessionName: "atc-\(id.replacingOccurrences(of: "-", with: ""))",
                            createdAt: Date(),
                            updatedAt: Date()
                        ))))
        }

        func getTerminal(_ input: Operations.GetTerminal.Input) async throws -> Operations.GetTerminal.Output {
            .ok(.init(body: .json(try require(terminals.first { $0.id == input.path.terminalId }))))
        }

        func updateTerminal(_ input: Operations.UpdateTerminal.Input) async throws -> Operations.UpdateTerminal.Output {
            guard case .json(let request) = input.body else { throw PreviewUnavailable() }
            var terminal = try require(terminals.first { $0.id == input.path.terminalId })
            terminal.name = request.name
            terminal.updatedAt = Date()
            return .ok(.init(body: .json(terminal)))
        }

        func deleteTerminal(_ input: Operations.DeleteTerminal.Input) async throws -> Operations.DeleteTerminal.Output {
            .noContent(.init())
        }

        // MARK: - Threads

        func listThreads(_ input: Operations.ListThreads.Input) async throws -> Operations.ListThreads.Output {
            let listing = input.query.archived == ._true ? archivedThreads : threads
            let projectID = input.query.projectId
            return .ok(
                .init(
                    body: .json(
                        listing.filter { projectID == nil || $0.projectId == projectID }
                    )))
        }

        func createThread(_ input: Operations.CreateThread.Input) async throws -> Operations.CreateThread.Output {
            guard case .json(let request) = input.body else { throw PreviewUnavailable() }
            let project = try require(projects.first { $0.id == request.projectId })
            return .ok(
                .init(
                    body: .json(
                        ATCThread(
                            id: Self.newID(),
                            projectId: project.id,
                            agentId: request.agentId,
                            kind: request.kind,
                            name: request.name,
                            workingDirectory: request.workingDirectory ?? project.defaultWorkingDirectory,
                            settings: request.agentId == .codex ? Self.codexSettings : Self.claudeSettings,
                            activityState: .unknown,
                            unread: false,
                            createdAt: Date(),
                            updatedAt: Date()
                        ))))
        }

        func getThread(_ input: Operations.GetThread.Input) async throws -> Operations.GetThread.Output {
            .ok(.init(body: .json(try thread(input.path.threadId))))
        }

        func updateThread(_ input: Operations.UpdateThread.Input) async throws -> Operations.UpdateThread.Output {
            guard case .json(let request) = input.body else { throw PreviewUnavailable() }
            var thread = try thread(input.path.threadId)
            if let name = request.name { thread.name = name }
            if let patch = request.settings {
                let catalog = thread.agentId == .codex ? Self.codexModels : Self.claudeModels
                thread.settings = thread.settings.applying(patch, catalog: catalog)
            }
            thread.updatedAt = Date()
            return .ok(.init(body: .json(thread)))
        }

        func deleteThread(_ input: Operations.DeleteThread.Input) async throws -> Operations.DeleteThread.Output {
            .noContent(.init())
        }

        func archiveThread(_ input: Operations.ArchiveThread.Input) async throws -> Operations.ArchiveThread.Output {
            var thread = try thread(input.path.threadId)
            thread.archivedAt = Date()
            thread.pinnedAt = nil
            return .ok(.init(body: .json(thread)))
        }

        func unarchiveThread(_ input: Operations.UnarchiveThread.Input) async throws
            -> Operations.UnarchiveThread.Output
        {
            var thread = try thread(input.path.threadId)
            thread.archivedAt = nil
            return .ok(.init(body: .json(thread)))
        }

        func pinThread(_ input: Operations.PinThread.Input) async throws -> Operations.PinThread.Output {
            var thread = try thread(input.path.threadId)
            thread.pinnedAt = Date()
            return .ok(.init(body: .json(thread)))
        }

        func unpinThread(_ input: Operations.UnpinThread.Input) async throws -> Operations.UnpinThread.Output {
            var thread = try thread(input.path.threadId)
            thread.pinnedAt = nil
            return .ok(.init(body: .json(thread)))
        }

        func markThreadViewed(_ input: Operations.MarkThreadViewed.Input) async throws
            -> Operations.MarkThreadViewed.Output
        {
            var thread = try thread(input.path.threadId)
            thread.unread = false
            return .ok(.init(body: .json(thread)))
        }

        func openThreadTerminal(
            _ input: Operations.OpenThreadTerminal.Input
        ) async throws -> Operations.OpenThreadTerminal.Output {
            let thread = try thread(input.path.threadId)
            if let linked = thread.linkedTerminalId,
                let terminal = terminals.first(where: { $0.id == linked })
            {
                return .ok(.init(body: .json(terminal)))
            }
            let id = Self.newID()
            return .ok(
                .init(
                    body: .json(
                        Terminal(
                            id: id,
                            projectId: thread.projectId,
                            threadId: thread.id,
                            initialWorkingDirectory: thread.workingDirectory,
                            status: .live,
                            sessionName: "atc-\(id.replacingOccurrences(of: "-", with: ""))",
                            createdAt: Date(),
                            updatedAt: Date()
                        ))))
        }

        func closeThreadTerminal(
            _ input: Operations.CloseThreadTerminal.Input
        ) async throws -> Operations.CloseThreadTerminal.Output {
            .ok(.init(body: .json(try thread(input.path.threadId))))
        }

        // MARK: - Agents

        // MARK: - Thread runtime (ATC-193) — previews show an idle, empty runtime.

        func promptThread(_ input: Operations.PromptThread.Input) async throws -> Operations.PromptThread.Output {
            .ok(.init(body: .json(.init(promptId: "0192f4c0-0000-7000-8000-000000000001", turnId: nil))))
        }

        func getThreadTranscript(
            _ input: Operations.GetThreadTranscript.Input
        ) async throws -> Operations.GetThreadTranscript.Output {
            .ok(.init(body: .json(.init(items: [], turns: [], seq: 0, snapshotVersion: 0, hasMore: false))))
        }

        func createThreadAttachment(
            _ input: Operations.CreateThreadAttachment.Input
        ) async throws -> Operations.CreateThreadAttachment.Output {
            .ok(
                .init(
                    body: .json(
                        .init(
                            id: "0192f4c0-0000-7000-8000-00000000a001", name: input.query.name ?? "image.png",
                            mediaType: .imagePng, byteSize: 0,
                            path: "/Users/dev/.local/share/atc/attachments/\(input.path.threadId)/a001.png",
                            createdAt: Date()))))
        }

        func getThreadAttachment(
            _ input: Operations.GetThreadAttachment.Input
        ) async throws -> Operations.GetThreadAttachment.Output {
            throw PreviewUnavailable()
        }

        func subscribeThreadEvents(
            _ input: Operations.SubscribeThreadEvents.Input
        ) async throws -> Operations.SubscribeThreadEvents.Output {
            .ok(.init(body: .textEventStream(HTTPBody(": connected\n\n"))))
        }

        func interruptThread(_ input: Operations.InterruptThread.Input) async throws
            -> Operations.InterruptThread.Output
        {
            .noContent(.init())
        }

        func listThreadRequests(
            _ input: Operations.ListThreadRequests.Input
        ) async throws -> Operations.ListThreadRequests.Output {
            .ok(.init(body: .json([])))
        }

        func answerThreadRequest(
            _ input: Operations.AnswerThreadRequest.Input
        ) async throws -> Operations.AnswerThreadRequest.Output {
            .noContent(.init())
        }

        func listThreadQueue(_ input: Operations.ListThreadQueue.Input) async throws
            -> Operations.ListThreadQueue.Output
        {
            .ok(.init(body: .json([])))
        }

        func deleteQueuedPrompt(
            _ input: Operations.DeleteQueuedPrompt.Input
        ) async throws -> Operations.DeleteQueuedPrompt.Output {
            .noContent(.init())
        }

        func listAgents(_ input: Operations.ListAgents.Input) async throws -> Operations.ListAgents.Output {
            .ok(.init(body: .json(agents)))
        }

        func listAgentCommands(
            _ input: Operations.ListAgentCommands.Input
        ) async throws -> Operations.ListAgentCommands.Output {
            .ok(
                .init(
                    body: .json([
                        .init(name: "review", description: "Review the current diff"),
                        .init(name: "commit", description: "Commit staged changes", argumentHint: "<message>"),
                    ])))
        }

        func searchFiles(_ input: Operations.SearchFiles.Input) async throws -> Operations.SearchFiles.Output {
            let files = ["Package.swift", "Sources/App/Main.swift", "Sources/App/Model.swift", "README.md"]
            let needle = (input.query.query ?? "").lowercased()
            let entries = files.filter { needle.isEmpty || $0.lowercased().contains(needle) }
                .map { Components.Schemas.FsFileEntry(path: $0, name: ($0 as NSString).lastPathComponent) }
            return .ok(.init(body: .json(.init(dir: input.query.dir, entries: entries, truncated: false))))
        }

        func listAgentModels(
            _ input: Operations.ListAgentModels.Input
        ) async throws -> Operations.ListAgentModels.Output {
            .ok(.init(body: .json(input.path.agentId == AgentID.codex.rawValue ? Self.codexModels : Self.claudeModels)))
        }

        func getAgent(_ input: Operations.GetAgent.Input) async throws -> Operations.GetAgent.Output {
            .ok(
                .init(
                    body: .json(
                        try require(
                            agents.first { $0.id.rawValue == input.path.agentId }
                        ))))
        }

        // MARK: - Lookups

        private func thread(_ id: String) throws -> ATCThread {
            try require((threads + archivedThreads).first { $0.id == id })
        }

        private func require<T>(_ value: T?) throws -> T {
            guard let value else { throw PreviewUnavailable() }
            return value
        }

        private static func newID() -> String {
            UUID().uuidString.lowercased()
        }
    }

    /// The preview client's only failure: a lookup that misses the fixtures.
    struct PreviewUnavailable: LocalizedError {
        var errorDescription: String? { "Not available in previews." }
    }

#endif

import ATCAppServerAPI
import ATCChat
import Foundation
import Testing

@testable import ATC

/// The New Thread composer's rules (ATC-216): chip defaults and re-targeting,
/// the ⌘-digit agent pick, the settings patch, and the first send's
/// create → patch → upload → prompt sequence — including a failed send
/// that keeps the created thread for the retry.
@Suite("New Thread draft")
struct NewThreadDraftTests {
    private func option(_ id: String, directory: String, connectionID: UUID = UUID()) -> ProjectOption {
        ProjectOption(
            ref: ProjectRef(connectionID: connectionID, projectID: id),
            project: Project(
                id: id, name: id, defaultWorkingDirectory: directory, createdAt: .distantPast, updatedAt: .distantPast),
            connectionName: "Local",
            label: id
        )
    }

    @Test("adopting a project moves the directory to its default and keeps the text; same project is a no-op")
    func adopt() {
        var draft = NewThreadDraft()
        draft.composer.text = "keep me"
        let app = option("app", directory: "/work/app")
        draft.adopt(app)
        #expect(draft.project == app.ref)
        #expect(draft.workingDirectory == "/work/app")
        draft.workingDirectory = "/work/app/sub"
        draft.adopt(app)
        #expect(draft.workingDirectory == "/work/app/sub")
        draft.created = ThreadRef(connectionID: app.ref.connectionID, threadID: "thr9")
        draft.adopt(option("web", directory: "/work/web"))
        #expect(draft.workingDirectory == "/work/web")
        #expect(draft.created == nil)
        #expect(draft.composer.text == "keep me")
    }

    @Test("⌘1…⌘9 name the Nth listed agent, available or not; past the list nothing")
    func agentShortcut() {
        let agents = [
            Agent(id: .codex, available: true, defaults: Fixtures.settings()),
            Agent(id: .claudeCode, available: false, reason: "not installed", defaults: Fixtures.settings()),
        ]
        #expect(NewThreadDraftRules.agent(forShortcut: 1, in: agents)?.id == .codex)
        #expect(NewThreadDraftRules.agent(forShortcut: 2, in: agents)?.available == false)
        #expect(NewThreadDraftRules.agent(forShortcut: 3, in: agents) == nil)
        #expect(NewThreadDraftRules.agent(forShortcut: 0, in: agents) == nil)
    }

    @Test("the settings patch carries only what differs from the base; nothing when equal")
    func settingsPatch() {
        let base = Fixtures.settings(model: "m1", reasoning: .high, mode: .chat, access: .auto)
        #expect(NewThreadDraftRules.settingsPatch(from: base, base: base) == nil)
        let patch = NewThreadDraftRules.settingsPatch(
            from: Fixtures.settings(model: "m2", reasoning: .high, mode: .plan, access: .auto), base: base)
        #expect(patch?.model == "m2")
        #expect(patch?.reasoning == nil)
        #expect(patch?.mode == .plan)
        #expect(patch?.access == nil)
    }

    @Test(
        "the first send creates once, patches only a changed setting, uploads, then prompts; a failed prompt keeps the thread"
    )
    func submission() async throws {
        let client = ScriptableAppServerClient()
        Fixtures.seed(client)
        let events = ScriptedEventStream()
        let test = try await makeModel(client: client, events: events)
        events.connect()
        await settle(until: { test.model.canMutate(connectionID: test.connectionID) })

        var draft = NewThreadDraft()
        draft.adopt(
            option("prj", directory: "/work/app", connectionID: test.connectionID))
        draft.agentID = .codex
        draft.composer.text = "  build it  "
        draft.composer.attachments = [PendingAttachment(data: Data([1, 2, 3]), mediaType: .imagePng, name: "a.png")]

        // The prompt is refused: the thread exists, the draft remembers it.
        client.promptThreadFailure = .init(
            _tag: .providerUnavailable, agentId: "codex", reason: "down", message: "down")
        let refused = await NewThreadSubmission.send(draft, when: nil, runtime: test.runtime)
        guard case .failure(let failure) = refused else { Issue.record("expected a failure"); return }
        #expect(failure.message.contains("down"))
        let created = try #require(failure.created)
        #expect(client.createdThreadRequests.count == 1)
        #expect(client.createdThreadRequests.first?.workingDirectory == "/work/app")
        #expect(client.settingsPatches.isEmpty)
        draft.created = created

        // The retry prompts that thread — no second creation — and a model
        // changed meanwhile is patched before the prompt goes.
        client.promptThreadFailure = nil
        let base = try #require(test.runtime.threads.thread(id: created.threadID)?.settings)
        draft.settings = Fixtures.settings(
            model: "other", reasoning: base.reasoning, mode: base.mode, access: base.access)
        let sent = await NewThreadSubmission.send(draft, when: nil, runtime: test.runtime)
        guard case .success(let ref) = sent else { Issue.record("expected success"); return }
        #expect(ref == created)
        #expect(client.createdThreadRequests.count == 1)
        #expect(client.settingsPatches.map(\.id) == [created.threadID])
        #expect(client.settingsPatches.first?.patch.model == "other")
        // The retry uploads again (the first upload stays a thread-scoped
        // orphan that dies with the thread); the prompt names the new id.
        #expect(client.uploads.map(\.threadID) == [created.threadID, created.threadID])
        let prompt = try #require(client.prompts.last)
        #expect(prompt.threadID == created.threadID)
        #expect(prompt.prompt == "build it")
        #expect(prompt.attachments == [client.uploads[1].attachment.id])
        #expect(draft.sent().composer.isEmpty)
        #expect(draft.sent().project == draft.project)
    }
}

@Suite("New Thread draft rules (review)")
struct NewThreadDraftRuleTests {
    @Test("changing the agent or directory forgets a thread a failed send created")
    func identityChangeForgetsCreated() {
        var draft = NewThreadDraft()
        let created = ThreadRef(connectionID: UUID(), threadID: "thr1")
        draft.created = created
        let unchanged = draft.agentID
        draft.agentID = unchanged
        #expect(draft.created == created)
        draft.agentID = .claudeCode
        #expect(draft.created == nil)
        draft.created = created
        draft.workingDirectory = "/elsewhere"
        #expect(draft.created == nil)
        var other = draft
        #expect(draft.targets(like: other))
        other.workingDirectory = "/again"
        #expect(!draft.targets(like: other))
    }

    @Test("a model change drops a reasoning level the new model lacks, to its default")
    func modelChangeFollowsCatalog() {
        let catalog = [
            AgentModel(
                value: "big", displayName: "Big", description: "", isDefault: true,
                supportedEffortLevels: [.low, .high], defaultEffortLevel: .high),
            AgentModel(
                value: "small", displayName: "Small", description: "", isDefault: false,
                supportedEffortLevels: [.low], defaultEffortLevel: .low),
            AgentModel(
                value: "plain", displayName: "Plain", description: "", isDefault: false,
                supportedEffortLevels: []),
        ]
        let start = ThreadSettings(model: "big", reasoning: .high, mode: .chat, access: .auto)
        let small = NewThreadDraftRules.applying(.init(model: "small"), to: start, catalog: catalog)
        #expect(small.model == "small")
        #expect(small.reasoning == .low)
        let plain = NewThreadDraftRules.applying(.init(model: "plain"), to: start, catalog: catalog)
        #expect(plain.reasoning == nil)
        let kept = NewThreadDraftRules.applying(
            .init(model: "small"), to: ThreadSettings(model: "big", reasoning: .low, mode: .chat, access: .auto),
            catalog: catalog)
        #expect(kept.reasoning == .low)
        let mode = NewThreadDraftRules.applying(.init(mode: .plan), to: start, catalog: catalog)
        #expect(mode.mode == .plan && mode.model == "big" && mode.reasoning == .high)
    }
}

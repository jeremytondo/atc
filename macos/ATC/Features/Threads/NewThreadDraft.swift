// The New Thread composer's state and rules (ATC-216): a thread is no
// longer configured in a sheet and created up front — the user types into
// an empty conversation whose chips (project · agent · directory · the
// composer's own settings controls) say what the first send creates. The
// draft is one app-wide slot (`AppModel.newThreadDraft`), like thread
// drafts, so leaving the composer and coming back finds it intact.
//
//   - The thread is created on the first send, once: a send that fails
//     after creation keeps the thread in `created`, and the next send
//     prompts it instead of making another. Settings are patched only
//     where the draft differs from what the thread holds — a draft that
//     never touched the controls costs no patch.
//   - Re-targeting the project (⌘N with another project in context) moves
//     the directory to that project's default; text, images, and the agent
//     stay. A created-but-unsent thread never follows a change of project,
//     agent, or directory — it would not match the chips — so such a change
//     forgets it and the next send creates anew.
//   - One send at a time, app-wide (`AppModel.newThreadSending`): the draft
//     is one slot shared by every window. A send's outcome is applied only
//     to the draft it was taken from; a draft edited meanwhile keeps its
//     edits, and a failure never pins a thread onto a re-targeted draft.

import ATCAppServerAPI
import ATCChat
import AppKit
import Foundation

struct NewThreadDraft: Equatable {
    var project: ProjectRef?
    var agentID: AgentID = .codex {
        didSet { if agentID != oldValue { created = nil } }
    }
    var workingDirectory = "" {
        didSet { if workingDirectory != oldValue { created = nil } }
    }
    /// What the thread starts with — the agent's defaults until a control
    /// is changed; nil before the registry has answered.
    var settings: ThreadSettings?
    var composer = ComposerDraft()
    /// The thread a failed send already created (see the header).
    var created: ThreadRef?

    /// Whether a send would do anything.
    var canSend: Bool {
        project != nil && !workingDirectory.isEmpty
            && (!composer.text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
                || !composer.attachments.isEmpty)
    }

    /// Point the draft at `option` (see the header's re-target rule).
    mutating func adopt(_ option: ProjectOption) {
        guard project != option.ref else { return }
        project = option.ref
        workingDirectory = option.project.defaultWorkingDirectory
        created = nil
    }

    /// Whether `other` would create the same thread: same project, agent,
    /// and directory — what a send's outcome may be applied to.
    func targets(like other: NewThreadDraft) -> Bool {
        project == other.project && agentID == other.agentID
            && workingDirectory == other.workingDirectory
    }

    /// The draft after a successful send: the composer empties, the thread
    /// is no longer pending, the chips stay for the next ⌘N.
    func sent() -> NewThreadDraft {
        var next = self
        next.composer = ComposerDraft()
        next.created = nil
        next.settings = nil
        return next
    }
}

enum NewThreadDraftRules {
    /// Settings shown before the registry has answered; never sent.
    static let placeholderSettings = ThreadSettings(model: "", mode: .chat, access: .auto)

    /// ⌘1…⌘9 (ATC-166): the Nth listed agent — an unavailable one is
    /// returned too, so the shortcut can answer with feedback, not silence.
    static func agent(forShortcut digit: Int, in agents: [Agent]) -> Agent? {
        guard (1...9).contains(digit), digit - 1 < agents.count else { return nil }
        return agents[digit - 1]
    }

    /// The patch that turns `base` (what the thread holds) into `settings`;
    /// nil when nothing differs.
    static func settingsPatch(from settings: ThreadSettings, base: ThreadSettings) -> ThreadSettingsPatch? {
        guard settings != base else { return nil }
        return ThreadSettingsPatch(
            model: settings.model == base.model ? nil : settings.model,
            reasoning: settings.reasoning == base.reasoning ? nil : settings.reasoning,
            mode: settings.mode == base.mode ? nil : settings.mode,
            access: settings.access == base.access ? nil : settings.access
        )
    }

    /// `patch` applied to `settings` with the server's own model rule (the
    /// draft has no server to apply it): a reasoning level the new model
    /// does not support gives way to that model's default, or to none.
    static func applying(
        _ patch: ThreadSettingsPatch, to settings: ThreadSettings, catalog: [AgentModel]?
    ) -> ThreadSettings {
        var next = settings
        if let model = patch.model, model != settings.model {
            next.model = model
            let entry = catalog?.first { $0.value == model }
            if let entry, let reasoning = settings.reasoning, !entry.supportedEffortLevels.contains(reasoning) {
                next.reasoning = entry.defaultEffortLevel
            }
        }
        if let reasoning = patch.reasoning { next.reasoning = reasoning }
        if let mode = patch.mode { next.mode = mode }
        if let access = patch.access { next.access = access }
        return next
    }

    /// The Connection's detected registry, or the built-in slugs before the
    /// first detection lands so the agent chip is never empty. Placeholder
    /// defaults never reach a thread: creation reads the server's.
    static func agents(detected: [Agent]) -> [Agent] {
        guard detected.isEmpty else { return detected }
        return AgentID.allCases.map {
            Agent(id: $0, available: true, defaults: placeholderSettings)
        }
    }
}

/// The first send, as one sequence against the draft's Connection (see the
/// header): create once, patch what differs, upload, prompt.
enum NewThreadSubmission {
    struct Failure: Error {
        let message: String
        /// The thread that exists by now, if creation got that far.
        let created: ThreadRef?
    }

    static func send(
        _ draft: NewThreadDraft, when: PromptWhen?, runtime: ConnectionRuntime
    ) async -> Result<ThreadRef, Failure> {
        guard let project = draft.project else {
            return .failure(Failure(message: "Pick a project first.", created: nil))
        }
        var created = draft.created
        do {
            if created == nil {
                let thread = try await runtime.threads.create(
                    .init(
                        projectId: project.projectID,
                        agentId: draft.agentID,
                        workingDirectory: draft.workingDirectory.trimmingCharacters(in: .whitespaces)
                    ))
                created = ThreadRef(connectionID: project.connectionID, threadID: thread.id)
            }
            guard let ref = created else { preconditionFailure("created above") }
            if let settings = draft.settings,
                let base = runtime.threads.thread(id: ref.threadID)?.settings,
                let patch = NewThreadDraftRules.settingsPatch(from: settings, base: base)
            {
                try await runtime.threads.updateSettings(id: ref.threadID, patch)
            }
            _ = try await ThreadPrompting.prompt(
                draft.composer.text.trimmingCharacters(in: .whitespacesAndNewlines),
                attachments: draft.composer.attachments,
                when: when,
                threadID: ref.threadID,
                client: runtime.client
            )
            return .success(ref)
        } catch {
            return .failure(Failure(message: error.localizedDescription, created: created))
        }
    }
}

// New Thread as a composer (ATC-216): the content area shows an empty
// conversation with the Chat composer at its tail and, above it, the chips
// that say what the first send creates — project, agent, directory; the
// model, mode, and access are the composer's own settings controls, acting
// on the draft instead of a thread. Nothing is created until the first
// send (`NewThreadSubmission`); the thread then opens in place and the
// conversation continues in `ThreadChatView`.
//
// Before that send, `@` search and `/` commands work off the chips: both
// endpoints are directory-driven, so they need no thread. ⌘1…⌘9 pick an
// agent while this view is shown (ATC-166; the window keyboard router
// forwards the digits here instead of jumping the sidebar). Leaving the
// view keeps the draft (`AppModel.newThreadDraft`); a failed send keeps it
// too and says why, inline.

import ATCAppServerAPI
import ATCChat
import ATCDesign
import SwiftUI

struct NewThreadComposerView: View {
    @Environment(AppModel.self) private var appModel
    @Environment(WindowState.self) private var windowState
    let context: NewThreadContext

    @AppStorage("newThreadLastAgentId") private var lastAgentRaw = ""
    @State private var directoryState: DirectoryCheckState = .idle
    @State private var isPickingProject = false
    @State private var isEditingDirectory = false
    @State private var sendError: String?

    private var draft: Binding<NewThreadDraft> {
        Binding(get: { appModel.newThreadDraft }, set: { appModel.newThreadDraft = $0 })
    }

    private var projectOptions: [ProjectOption] {
        appModel.threadList(filter: .all).projects
    }

    private var selectedOption: ProjectOption? {
        projectOptions.first { $0.ref == appModel.newThreadDraft.project }
    }

    private var runtime: ConnectionRuntime? {
        appModel.newThreadDraft.project.flatMap { appModel.runtime(id: $0.connectionID) }
    }

    private var agents: [Agent] { appModel.newThreadAgents() }

    private var agent: Agent? { agents.first { $0.id == appModel.newThreadDraft.agentID } }

    /// What the composer's controls show: the draft's settings, or the
    /// agent's defaults until the draft has its own.
    private var subject: ComposerSubject {
        ComposerSubject(
            agentId: appModel.newThreadDraft.agentID,
            settings: appModel.newThreadDraft.settings ?? agent?.defaults
                ?? NewThreadDraftRules.placeholderSettings
        )
    }

    var body: some View {
        VStack(spacing: Spacing.sm) {
            Spacer()
            Text("New thread")
                .font(.title2.weight(.semibold))
            Text("Say what to do. The thread is created when you send.")
                .font(.callout)
                .foregroundStyle(.secondary)
            Spacer()
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(AppColors.canvas)
        .safeAreaBar(edge: .bottom) { bottomStack }
        .task(id: context) { adoptContext() }
        .task(id: appModel.newThreadDraft.project?.connectionID) {
            await runtime?.agents.refresh()
        }
        // Keyed on the runtime too: another Connection (or a rebuilt one) has
        // its own catalog, and the draft's settings belong to that catalog.
        .task(id: RuntimeAgent(runtime: runtime.map(ObjectIdentifier.init), agent: appModel.newThreadDraft.agentID)) {
            runtime?.agents.loadModels(for: appModel.newThreadDraft.agentID)
        }
        .onChange(of: runtime.map(ObjectIdentifier.init)) { _, _ in draft.wrappedValue.settings = nil }
        // Detection landing (or a Connection change) applies the
        // preserve/adopt rule to the agent chip.
        .onChange(of: agents) { _, agents in
            draft.wrappedValue.agentID = NewThreadLauncherModel.resolvedAgent(
                preferring: appModel.newThreadDraft.agentID, in: agents)
        }
        // A new agent starts from its own defaults, not the last agent's.
        .onChange(of: appModel.newThreadDraft.agentID) { _, _ in draft.wrappedValue.settings = nil }
    }

    /// ⌘N with a project in context re-targets the draft; otherwise the
    /// draft keeps its project, or takes the first offered one. A pristine
    /// draft starts on the last-used agent; a kept one keeps its agent
    /// (resolved against availability).
    private func adoptContext() {
        let options = projectOptions
        let pristine = appModel.newThreadDraft.project == nil
        if let ref = context.projectRef, let option = options.first(where: { $0.ref == ref }) {
            draft.wrappedValue.adopt(option)
        } else if selectedOption == nil, let first = options.first {
            draft.wrappedValue.adopt(first)
        }
        draft.wrappedValue.agentID =
            pristine
            ? NewThreadLauncherModel.initialAgent(lastUsedRawValue: lastAgentRaw, agents: agents)
            : NewThreadLauncherModel.resolvedAgent(preferring: appModel.newThreadDraft.agentID, in: agents)
    }

    // MARK: - Bottom stack

    private var bottomStack: some View {
        GlassEffectContainer {
            VStack(spacing: Spacing.sm) {
                chips
                ChatComposer(
                    text: draft.composer.text,
                    attachments: draft.composer.attachments,
                    subject: subject,
                    models: runtime?.agents.models(for: appModel.newThreadDraft.agentID),
                    modelsError: runtime?.agents.modelErrors[appModel.newThreadDraft.agentID],
                    isTurnRunning: false,
                    error: sendError,
                    history: [],
                    commands: runtime?.agents.commands(
                        for: appModel.newThreadDraft.agentID, dir: appModel.newThreadDraft.workingDirectory),
                    yieldsFocus: false,
                    focusRequest: windowState.contentFocusRequest,
                    searchFiles: searchFiles,
                    loadCommands: {
                        runtime?.agents.loadCommands(
                            for: appModel.newThreadDraft.agentID, dir: appModel.newThreadDraft.workingDirectory)
                    },
                    send: send,
                    stop: {},
                    updateSettings: { patch in
                        draft.wrappedValue.settings = NewThreadDraftRules.applying(
                            patch, to: subject.settings,
                            catalog: runtime?.agents.models(for: appModel.newThreadDraft.agentID))
                    },
                    reloadModels: { runtime?.agents.loadModels(for: appModel.newThreadDraft.agentID) }
                )
                .disabled(appModel.newThreadSending)
            }
        }
        .padding(.horizontal, Spacing.xxl)
        .padding(.top, Spacing.md)
        .padding(.bottom, Spacing.lg)
        .frame(maxWidth: 820)
        .frame(maxWidth: .infinity)
    }

    /// The three things the composer's own controls do not cover, as quiet
    /// chips in the composer's idiom.
    private var chips: some View {
        HStack(spacing: Spacing.sm) {
            Button {
                isPickingProject = true
            } label: {
                chipLabel(systemImage: "folder", title: selectedOption?.project.name ?? "Project")
            }
            .popover(isPresented: $isPickingProject, arrowEdge: .top) {
                ProjectPicker(
                    options: projectOptions, context: context.projectRef, selected: appModel.newThreadDraft.project
                ) {
                    draft.wrappedValue.adopt($0)
                    isPickingProject = false
                }
            }
            .help("Project")
            .accessibilityLabel("Project: \(selectedOption?.project.name ?? "none")")

            PopupMenuButton(entries: agentEntries, appearance: .accessoryBar) {
                chipLabel(
                    systemImage: appModel.newThreadDraft.agentID.systemImage,
                    title: appModel.newThreadDraft.agentID.displayName)
            }
            .help("Agent (⌘1–⌘9)")
            .accessibilityLabel("Agent: \(appModel.newThreadDraft.agentID.displayName)")

            Button {
                isEditingDirectory = true
            } label: {
                chipLabel(
                    systemImage: "terminal",
                    title: (appModel.newThreadDraft.workingDirectory as NSString).abbreviatingWithTildeInPath)
            }
            .popover(isPresented: $isEditingDirectory, arrowEdge: .top) {
                WorkingDirectoryField(
                    label: "Working Directory",
                    path: draft.workingDirectory,
                    client: runtime?.client,
                    connectionID: runtime?.id,
                    state: $directoryState
                )
                .frame(width: 460)
                .padding(Spacing.lg)
            }
            .help("Working directory")
            .accessibilityLabel("Directory: \(appModel.newThreadDraft.workingDirectory)")
            Spacer(minLength: 0)
        }
        .buttonStyle(.accessoryBar)
        // What a send is creating must not move under it.
        .disabled(appModel.newThreadSending)
        .padding(.horizontal, Spacing.sm)
    }

    private func chipLabel(systemImage: String, title: String) -> some View {
        HStack(spacing: Spacing.xs) {
            Image(systemName: systemImage)
            Text(title).foregroundStyle(.primary)
            Image(systemName: "chevron.down")
                .font(.caption2.weight(.semibold))
        }
        .font(.callout)
        .foregroundStyle(.secondary)
        .lineLimit(1)
        .truncationMode(.middle)
        .frame(maxWidth: 260)
        .padding(.horizontal, Spacing.xs)
        .padding(.vertical, 2)
    }

    /// Available agents pick; an unavailable one shows why, as a header.
    private var agentEntries: [PopupMenuEntry] {
        agents.enumerated().map { index, agent in
            guard agent.available else {
                return .header("\(agent.id.displayName) — \(agent.reason ?? "Unavailable")")
            }
            let shortcut = index < 9 ? "  ⌘\(index + 1)" : ""
            return .item(
                title: agent.id.displayName + shortcut,
                isSelected: agent.id == appModel.newThreadDraft.agentID
            ) { draft.wrappedValue.agentID = agent.id }
        }
    }

    // MARK: - Sending

    /// The first send (`NewThreadSubmission`), one at a time app-wide: on
    /// success the new thread opens in place and the draft empties — unless
    /// it was edited meanwhile, in which case the edits stay; a failure
    /// keeps everything and says why, pinning the created thread only on a
    /// draft that still targets it (see the header).
    private func send(when: PromptWhen?) {
        let draft = appModel.newThreadDraft
        guard !appModel.newThreadSending, draft.canSend, let runtime else { return }
        guard agent?.available != false else {
            sendError = "\(draft.agentID.displayName) is not available on this Connection."
            return
        }
        appModel.newThreadSending = true
        sendError = nil
        Task {
            defer { appModel.newThreadSending = false }
            switch await NewThreadSubmission.send(draft, when: when, runtime: runtime) {
            case .success(let ref):
                lastAgentRaw = draft.agentID.rawValue
                // The prompt went: the live draft empties unless it was
                // re-targeted meanwhile (then its text is a new intent).
                if appModel.newThreadDraft.targets(like: draft) {
                    appModel.newThreadDraft = appModel.newThreadDraft.sent()
                }
                await windowState.openThread(ref, in: appModel)
            case .failure(let failure):
                if appModel.newThreadDraft.targets(like: draft) {
                    appModel.newThreadDraft.created = failure.created
                }
                sendError = failure.message
            }
        }
    }

    private func searchFiles(_ query: String) async -> Components.Schemas.FsFilesResponse? {
        await runtime?.searchFiles(dir: appModel.newThreadDraft.workingDirectory, query: query)
    }
}

/// What the model catalog read is keyed on: one Connection's runtime, one agent.
private struct RuntimeAgent: Hashable {
    let runtime: ObjectIdentifier?
    let agent: AgentID
}

/// The project chip's popover: the launcher's search-first list, keyboard
/// first (↑/↓ move, Return picks).
private struct ProjectPicker: View {
    let options: [ProjectOption]
    let context: ProjectRef?
    let selected: ProjectRef?
    let pick: (ProjectOption) -> Void

    @Environment(AppModel.self) private var appModel
    @State private var query = ""
    @State private var highlighted: ProjectRef?
    @FocusState private var searchIsFocused: Bool

    var body: some View {
        let rows = NewThreadLauncherModel.rows(options: options, query: query, contextProject: context)
        VStack(spacing: Spacing.sm) {
            HStack(spacing: Spacing.sm) {
                Image(systemName: "magnifyingglass").foregroundStyle(.secondary)
                TextField("Search projects…", text: $query)
                    .textFieldStyle(.plain)
                    .autocorrectionDisabled()
                    .focused($searchIsFocused)
                    .accessibilityLabel("Project search")
                    .onSelectionMoveKeys { offset in
                        highlighted = NewThreadLauncherModel.movedSelection(from: highlighted, by: offset, in: rows)
                        return .handled
                    }
                    .onSubmit {
                        if let row = rows.first(where: { $0.id == highlighted }) ?? rows.first { pick(row.option) }
                    }
            }
            .padding(.horizontal, Spacing.sm)
            ScrollView {
                LazyVStack(spacing: 2) {
                    ForEach(rows) { row in
                        projectRow(row, isHighlighted: row.id == (highlighted ?? selected))
                            .onTapGesture { pick(row.option) }
                    }
                }
            }
            .frame(height: 32 * 6)
        }
        .frame(width: 420)
        .padding(Spacing.md)
        .defaultFocus($searchIsFocused, true)
        .onAppear { highlighted = selected }
        .onChange(of: rows.map(\.id)) { _, ids in
            if !ids.contains(where: { $0 == highlighted }) { highlighted = ids.first }
        }
    }

    private func projectRow(_ row: NewThreadLauncherModel.Row, isHighlighted: Bool) -> some View {
        HStack(spacing: Spacing.sm) {
            StatusDot(reachability: appModel.reachability(of: row.id.connectionID))
            HighlightedText.title(row.option.project.name, ranges: row.nameRanges)
                .font(.callout.weight(.medium))
                .foregroundStyle(isHighlighted ? Color.white : Color.primary)
                .lineLimit(1)
                .layoutPriority(1)
            Text(row.option.project.defaultWorkingDirectory)
                .font(.caption)
                .foregroundStyle(isHighlighted ? Color.white.opacity(0.75) : Color.secondary)
                .lineLimit(1)
                .truncationMode(.middle)
            Spacer(minLength: Spacing.sm)
            Text(row.option.connectionName)
                .font(.caption.weight(.medium))
                .foregroundStyle(isHighlighted ? Color.white : Color.secondary)
        }
        .padding(.horizontal, Spacing.md)
        .frame(height: 32)
        .background {
            RoundedRectangle(cornerRadius: Radius.chip, style: .continuous)
                .fill(isHighlighted ? Color.accentColor : .clear)
        }
        .contentShape(Rectangle())
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("\(row.option.project.name), \(row.option.connectionName)")
        .accessibilityAddTraits(isHighlighted ? .isSelected : [])
    }
}

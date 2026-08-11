// The New Thread launcher for `POST /threads`: a search-first sheet where
// Project search dominates and the Agent and Working Directory stay visible
// below it. Threads are auto-named after their first prompt, so there is no
// Name field.
//
// Keyboard model: the search field keeps focus while ↑/↓/Ctrl-N/Ctrl-P move
// the highlighted Project; ⌘1…⌘9 select Agents (never create); Shift-⌘-G
// focuses the directory field; Return creates via the scaffold's default
// action and Escape cancels. Because the sheet is the key window, the global
// ⌘-digit sidebar jumps cannot fire underneath it.
//
// Agent availability is detected, never stored, so the sheet refreshes the
// selected Connection's registry and disables providers the server reports
// as missing — with the server's own actionable reason, which is the only
// place the user learns what to install.

import ATCAppServerAPI
import SwiftUI

struct NewThreadSheet: View {
    @Environment(AppModel.self) private var appModel
    @Environment(WindowState.self) private var windowState
    let context: NewThreadContext

    @AppStorage("newThreadLastAgentId") private var lastAgentRaw = ""

    @State private var query = ""
    @State private var selectedProject: ProjectRef?
    @State private var agentID: AgentID = .codex
    @State private var workingDirectory = ""
    @State private var directoryState: DirectoryCheckState = .idle
    @State private var isSubmitting = false
    @State private var submitError: String?
    /// Debounces the unavailable-agent beep so a held shortcut's key repeat
    /// does not stutter it.
    @State private var isFeedbackCoolingDown = false
    @FocusState private var searchIsFocused: Bool
    @FocusState private var directoryIsFocused: Bool

    var body: some View {
        let rows = NewThreadLauncherModel.rows(
            options: projectOptions,
            query: query,
            contextProject: context.projectRef
        )
        SheetScaffold(
            title: "New Thread",
            systemImage: "plus.bubble",
            primaryLabel: "Create Thread",
            isBusy: isSubmitting,
            canSubmit: canSubmit,
            wrapsContentInForm: false,
            onCancel: { windowState.newThreadContext = nil },
            onSubmit: { Task { await submit() } }
        ) {
            VStack(spacing: 0) {
                searchField(rows: rows)
                Divider()
                resultsList(rows: rows)
                Divider()
                configurationRows
            }
        }
        .frame(width: 560, height: 500)
        .background(hiddenShortcutButtons)
        .task {
            searchIsFocused = true
            agentID = NewThreadLauncherModel.initialAgent(
                lastUsedRawValue: lastAgentRaw,
                agents: agents
            )
            if selectedProject == nil {
                selectProject(rows.first?.id, in: rows)
            }
        }
        // Detection is per Connection; re-run it whenever the highlighted
        // Project moves to another one.
        .task(id: selectedProject?.connectionID) {
            await selectedRuntime?.agents.refresh()
        }
        // Applies the preserve/adopt rule when detection lands or the
        // highlighted Project changes the offered registry.
        .onChange(of: agents) {
            agentID = NewThreadLauncherModel.resolvedAgent(preferring: agentID, in: agents)
        }
        // Store refreshes or a query edit can drop the highlighted row;
        // fall back to the top of the filtered results.
        .onChange(of: rows.map(\.id)) {
            if let selectedProject, rows.contains(where: { $0.id == selectedProject }) { return }
            selectProject(rows.first?.id, in: rows)
        }
    }

    // MARK: - Search and results

    private func searchField(rows: [NewThreadLauncherModel.Row]) -> some View {
        HStack(spacing: Spacing.sm) {
            Image(systemName: "magnifyingglass")
                .foregroundStyle(.secondary)
            TextField("Search projects…", text: $query)
                .textFieldStyle(.plain)
                .autocorrectionDisabled()
                .focused($searchIsFocused)
                .accessibilityLabel("Project search")
                .onKeyPress(.downArrow) { moveSelection(1, in: rows) }
                .onKeyPress(.upArrow) { moveSelection(-1, in: rows) }
                .onKeyPress(keys: ["n"], phases: .down) { press in
                    guard press.modifiers == .control else { return .ignored }
                    return moveSelection(1, in: rows)
                }
                .onKeyPress(keys: ["p"], phases: .down) { press in
                    guard press.modifiers == .control else { return .ignored }
                    return moveSelection(-1, in: rows)
                }
        }
        .font(.title3)
        .padding(.horizontal, Spacing.md)
        .padding(.vertical, Spacing.md)
    }

    @ViewBuilder
    private func resultsList(rows: [NewThreadLauncherModel.Row]) -> some View {
        if rows.isEmpty {
            Text(projectOptions.isEmpty ? "No Projects" : "No matching projects")
                .font(.callout)
                .foregroundStyle(.secondary)
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        } else {
            ScrollViewReader { proxy in
                ScrollView {
                    LazyVStack(spacing: 2) {
                        ForEach(rows) { row in
                            resultRow(row)
                                .id(row.id)
                        }
                    }
                    .padding(Spacing.xs)
                }
                .onChange(of: selectedProject) {
                    if let selectedProject {
                        proxy.scrollTo(selectedProject, anchor: .center)
                    }
                }
            }
        }
    }

    private func resultRow(_ row: NewThreadLauncherModel.Row) -> some View {
        let isSelected = selectedProject == row.id
        return HStack(spacing: Spacing.sm) {
            highlightedTitle(row.option.project.name, ranges: row.nameRanges)
                .font(.callout)
                .lineLimit(1)
            Spacer(minLength: Spacing.md)
            Text(row.option.connectionName)
                .font(.caption)
                .foregroundStyle(.secondary)
            Text(row.option.project.defaultWorkingDirectory)
                .font(.caption)
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .truncationMode(.middle)
                .frame(maxWidth: 200, alignment: .trailing)
        }
        .padding(.horizontal, Spacing.md)
        .padding(.vertical, 7)
        .background {
            RoundedRectangle(cornerRadius: 6)
                .fill(isSelected ? Color.accentColor.opacity(0.65) : .clear)
        }
        .contentShape(Rectangle())
        .onTapGesture { selectProject(row.id, in: [row]) }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(
            "\(row.option.project.name), \(row.option.connectionName), "
                + row.option.project.defaultWorkingDirectory
        )
        .accessibilityAddTraits(isSelected ? .isSelected : [])
        .accessibilityAction { selectProject(row.id, in: [row]) }
    }

    private func highlightedTitle(
        _ title: String,
        ranges: [Range<String.Index>]
    ) -> Text {
        var text = Text("")
        var cursor = title.startIndex
        for range in ranges.sorted(by: { $0.lowerBound < $1.lowerBound }) {
            text = text + Text(String(title[cursor..<range.lowerBound]))
            text = text + Text(String(title[range])).bold()
            cursor = range.upperBound
        }
        return text + Text(String(title[cursor...]))
    }

    // MARK: - Agent and directory rows

    private var configurationRows: some View {
        VStack(alignment: .leading, spacing: Spacing.md) {
            Grid(alignment: .topLeading, horizontalSpacing: Spacing.md, verticalSpacing: Spacing.md) {
                GridRow {
                    rowLabel("Agent")
                    VStack(alignment: .leading, spacing: Spacing.xs) {
                        HStack(spacing: Spacing.sm) {
                            ForEach(Array(agents.enumerated()), id: \.element.id) { index, agent in
                                agentButton(agent, index: index)
                            }
                        }
                        ForEach(agents.filter { !$0.available }, id: \.id) { agent in
                            Label(
                                "\(agent.id.displayName) — \(agent.reason ?? "Unavailable")",
                                systemImage: "exclamationmark.triangle"
                            )
                            .font(.caption)
                            .foregroundStyle(.secondary)
                        }
                    }
                }
                GridRow {
                    rowLabel("Directory")
                    WorkingDirectoryField(
                        label: "Working Directory",
                        path: $workingDirectory,
                        client: selectedRuntime?.client,
                        connectionID: selectedRuntime?.id,
                        state: $directoryState,
                        isFocused: $directoryIsFocused
                    )
                    .frame(maxWidth: .infinity)
                }
            }
            if let submitError {
                Label(submitError, systemImage: "exclamationmark.triangle")
                    .foregroundStyle(.red)
                    .font(.callout)
            }
        }
        .padding(Spacing.md)
    }

    /// Fixed-width row labels keep the Agent and Directory cells aligned.
    private func rowLabel(_ text: String) -> some View {
        Text(text)
            .foregroundStyle(.secondary)
            .frame(width: 70, alignment: .leading)
            .padding(.top, Spacing.xs)
    }

    private func agentButton(_ agent: Agent, index: Int) -> some View {
        let isSelected = agentID == agent.id
        return Button {
            select(agent)
        } label: {
            HStack(spacing: Spacing.xs) {
                Image(systemName: agent.id.systemImage)
                Text(agent.id.displayName)
                if let shortcut = shortcutLabel(at: index) {
                    Text(shortcut)
                        .font(.caption.monospaced().weight(.medium))
                        .foregroundStyle(isSelected ? .primary : .secondary)
                }
            }
            .padding(.horizontal, Spacing.sm)
            .padding(.vertical, Spacing.xs)
            .background {
                RoundedRectangle(cornerRadius: 6)
                    .fill(isSelected ? Color.accentColor.opacity(0.35) : Color.primary.opacity(0.06))
            }
            .overlay {
                RoundedRectangle(cornerRadius: 6)
                    .stroke(
                        isSelected ? Color.accentColor : Color.primary.opacity(0.12),
                        lineWidth: 1
                    )
            }
        }
        .buttonStyle(.plain)
        .disabled(!agent.available)
        .opacity(agent.available ? 1 : 0.5)
        .accessibilityLabel(agentAccessibilityLabel(agent, index: index))
        .accessibilityAddTraits(isSelected ? .isSelected : [])
    }

    /// The ⌘-digit selectors live on hidden always-enabled buttons so an
    /// unavailable Agent's shortcut can still answer with feedback — a
    /// disabled visible button would swallow it silently.
    private var hiddenShortcutButtons: some View {
        VStack {
            ForEach(Array(agents.prefix(9).enumerated()), id: \.element.id) { index, agent in
                Button("") { select(agent) }
                    .keyboardShortcut(KeyEquivalent(Character("\(index + 1)")), modifiers: .command)
            }
            Button("") { directoryIsFocused = true }
                .keyboardShortcut("g", modifiers: [.command, .shift])
        }
        .frame(width: 0, height: 0)
        .opacity(0)
        .accessibilityHidden(true)
    }

    private func shortcutLabel(at index: Int) -> String? {
        index < 9 ? "⌘\(index + 1)" : nil
    }

    private func agentAccessibilityLabel(_ agent: Agent, index: Int) -> String {
        var parts = [agent.id.displayName]
        if !agent.available {
            parts.append("Unavailable — \(agent.reason ?? "Unavailable")")
        }
        if index < 9 {
            parts.append("Command \(index + 1)")
        }
        return parts.joined(separator: ", ")
    }

    // MARK: - Derived state

    private var projectOptions: [ProjectOption] {
        ThreadListModel(
            inputs: appModel.runtimes.map(ThreadListModel.ConnectionInput.init(runtime:)),
            filter: .all
        ).projects
    }

    private var selectedRuntime: ConnectionRuntime? {
        selectedProject.flatMap { appModel.runtime(id: $0.connectionID) }
    }

    /// The Connection's detected registry, or the built-in slugs before the
    /// first detection lands so the row is never empty.
    private var agents: [Agent] {
        let detected = selectedRuntime?.agents.agents ?? []
        guard detected.isEmpty else { return detected }
        return AgentID.allCases.map {
            Agent(id: $0, available: true)
        }
    }

    private var canSubmit: Bool {
        !isSubmitting
            && selectedProject != nil
            && directoryState.isAvailable
            && agents.first { $0.id == agentID }?.available == true
    }

    // MARK: - Actions

    private func moveSelection(
        _ offset: Int,
        in rows: [NewThreadLauncherModel.Row]
    ) -> KeyPress.Result {
        selectProject(
            NewThreadLauncherModel.movedSelection(from: selectedProject, by: offset, in: rows),
            in: rows
        )
        return .handled
    }

    /// Changing the highlighted Project resets the directory to its default,
    /// discarding any override — the field always shows where the new thread
    /// would run.
    private func selectProject(_ ref: ProjectRef?, in rows: [NewThreadLauncherModel.Row]) {
        guard selectedProject != ref else { return }
        selectedProject = ref
        workingDirectory = rows.first { $0.id == ref }?
            .option.project.defaultWorkingDirectory ?? ""
        if let row = rows.first(where: { $0.id == ref }) {
            AccessibilityNotification.Announcement(
                "\(row.option.project.name), \(row.option.connectionName)"
            ).post()
        }
    }

    private func select(_ agent: Agent) {
        guard agent.available else {
            guard !isFeedbackCoolingDown else { return }
            isFeedbackCoolingDown = true
            NSSound.beep()
            Task {
                try? await Task.sleep(for: .milliseconds(400))
                isFeedbackCoolingDown = false
            }
            return
        }
        agentID = agent.id
    }

    private func submit() async {
        guard let ref = selectedProject, let runtime = selectedRuntime, canSubmit else { return }
        isSubmitting = true
        defer { isSubmitting = false }
        do {
            let thread = try await runtime.threads.create(.init(
                projectId: ref.projectID,
                agentId: agentID,
                workingDirectory: workingDirectory.trimmingCharacters(in: .whitespaces)
            ))
            submitError = nil
            lastAgentRaw = agentID.rawValue
            windowState.threadCreated(
                ThreadRef(connectionID: ref.connectionID, threadID: thread.id),
                in: appModel
            )
        } catch {
            submitError = error.localizedDescription
        }
    }
}

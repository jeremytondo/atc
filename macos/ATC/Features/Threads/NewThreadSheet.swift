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

    private static let agentShortcutKeys: Set<KeyEquivalent> = Set(
        (1...9).map { KeyEquivalent(Character("\($0)")) }
    )

    /// The sheet's one horizontal rhythm. Every container — search field,
    /// result row, configuration grid — sits `gutter` in from the sheet edge,
    /// and its own content sits `Spacing.md` inside that, so the search icon,
    /// the rows' status dots, and the section header all share a left edge and
    /// a selected row is exactly as wide as the search field above it.
    private static let gutter = Spacing.md

    /// The results list shows exactly this many rows; more scroll within it.
    private static let visibleRowCount = 6
    private static let rowHeight: CGFloat = 32
    private static let rowSpacing: CGFloat = 2
    private static let resultsHeight: CGFloat =
        rowHeight * CGFloat(visibleRowCount)
        + rowSpacing * CGFloat(visibleRowCount - 1)
        + Spacing.xs * 2

    @AppStorage("newThreadLastAgentId") private var lastAgentRaw = ""

    @State private var query = ""
    @State private var selectedProject: ProjectRef?
    /// The row the results list is parked on. Separate from
    /// `selectedProject` because SwiftUI writes the scrolled-to row into it.
    @State private var scrolledProject: ProjectRef?
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
            showsHeader: false,
            onCancel: { windowState.newThreadContext = nil },
            onSubmit: { Task { await submit() } }
        ) {
            VStack(spacing: 0) {
                searchField(rows: rows)
                resultsHeader
                resultsList(rows: rows)
                    .frame(height: Self.resultsHeight)
                Divider()
                configurationRows
            }
        }
        .frame(width: 560)
        .defaultFocus($searchIsFocused, true)
        // ⌘1…⌘9 select Agents, handled on the sheet rather than on the Agent
        // buttons so an unavailable Agent's shortcut still answers with
        // feedback instead of being swallowed by a disabled control.
        .onKeyPress(keys: Self.agentShortcutKeys, phases: .down) { press in
            // Mask to the device-independent modifiers: `press.modifiers`
            // also carries capsLock/numericPad, which must not defeat ⌘1–9.
            guard press.modifiers.intersection([.command, .shift, .option, .control]) == .command,
                let digit = press.key.character.wholeNumberValue,
                digit - 1 < agents.count
            else { return .ignored }
            select(agents[digit - 1])
            return .handled
        }
        // Both cases: `press.key` preserves Shift, so a real ⇧⌘G arrives as "G".
        .onKeyPress(keys: ["g", "G"], phases: .down) { press in
            guard press.modifiers.intersection([.command, .shift, .option, .control]) == [.command, .shift]
            else { return .ignored }
            directoryIsFocused = true
            return .handled
        }
        .task {
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
                .onSelectionMoveKeys { moveSelection($0, in: rows) }
        }
        .font(.title3)
        .padding(.horizontal, Spacing.md)
        .padding(.vertical, Spacing.sm + 2)
        .background(
            Surface.chip,
            in: RoundedRectangle(cornerRadius: Radius.chip, style: .continuous)
        )
        .overlay {
            RoundedRectangle(cornerRadius: Radius.chip, style: .continuous)
                .stroke(Surface.chipBorder, lineWidth: 1)
        }
        .padding(.horizontal, Self.gutter)
        .padding(.top, Spacing.md)
        .padding(.bottom, Spacing.sm)
    }

    /// The list is alphabetical (context Project first), so the header says
    /// what it shows rather than borrowing the mock's "Recent".
    private var resultsHeader: some View {
        Text(
            query.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
                ? "Projects" : "Results"
        )
        .font(.caption.weight(.semibold))
        .foregroundStyle(.secondary)
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, Self.gutter + Spacing.md)
        .padding(.bottom, Spacing.xs)
        .accessibilityAddTraits(.isHeader)
    }

    @ViewBuilder
    private func resultsList(rows: [NewThreadLauncherModel.Row]) -> some View {
        if rows.isEmpty {
            Text(projectOptions.isEmpty ? "No Projects" : "No matching projects")
                .font(.callout)
                .foregroundStyle(.secondary)
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        } else {
            ScrollView {
                LazyVStack(spacing: Self.rowSpacing) {
                    ForEach(rows) { row in
                        resultRow(row)
                            .id(row.id)
                    }
                }
                .scrollTargetLayout()
                .padding(.horizontal, Self.gutter)
                .padding(.vertical, Spacing.xs)
            }
            .scrollPosition(id: $scrolledProject, anchor: .center)
            .onChange(of: selectedProject) { scrolledProject = selectedProject }
        }
    }

    private func resultRow(_ row: NewThreadLauncherModel.Row) -> some View {
        let isSelected = selectedProject == row.id
        return HStack(spacing: Spacing.sm) {
            StatusDot(reachability: appModel.reachability(of: row.id.connectionID))
            HighlightedText.title(row.option.project.name, ranges: row.nameRanges)
                .font(.callout.weight(.medium))
                .foregroundStyle(isSelected ? Color.white : Color.primary)
                .lineLimit(1)
                .layoutPriority(1)
            Text(row.option.project.defaultWorkingDirectory)
                .font(.caption)
                .foregroundStyle(isSelected ? Color.white.opacity(0.75) : Color.secondary)
                .lineLimit(1)
                .truncationMode(.middle)
            Spacer(minLength: Spacing.sm)
            Text(row.option.connectionName)
                .font(.caption.weight(.medium))
                .foregroundStyle(isSelected ? Color.white : Color.secondary)
                .padding(.horizontal, Spacing.sm)
                .padding(.vertical, 2)
                .background(
                    isSelected ? AnyShapeStyle(.white.opacity(0.2)) : AnyShapeStyle(Surface.raised),
                    in: RoundedRectangle(cornerRadius: Radius.control, style: .continuous)
                )
        }
        .padding(.horizontal, Spacing.md)
        .frame(height: Self.rowHeight)
        .background {
            RoundedRectangle(cornerRadius: Radius.chip, style: .continuous)
                .fill(isSelected ? Color.accentColor : .clear)
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

    // MARK: - Agent and directory rows

    private var configurationRows: some View {
        VStack(alignment: .leading, spacing: Spacing.md) {
            Grid(
                alignment: .leadingFirstTextBaseline,
                horizontalSpacing: Spacing.md,
                verticalSpacing: Spacing.lg
            ) {
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
        .padding(.horizontal, Self.gutter)
        .padding(.vertical, Spacing.lg)
    }

    /// Fixed-width row labels keep the Agent and Directory cells aligned, and
    /// trail toward the controls the way a macOS form's labels do. The Grid's
    /// baseline alignment does the vertical work, so no nudging here.
    private func rowLabel(_ text: String) -> some View {
        Text(text)
            .foregroundStyle(.secondary)
            .frame(width: 70, alignment: .trailing)
    }

    private func agentButton(_ agent: Agent, index: Int) -> some View {
        let isSelected = agentID == agent.id
        return Button {
            select(agent)
        } label: {
            HStack(spacing: Spacing.sm) {
                // Label rather than a hand-spaced icon + Text: the system's
                // own icon-to-title gap is what makes these read as one chip.
                Label(agent.id.displayName, systemImage: agent.id.systemImage)
                if let shortcut = shortcutLabel(at: index) {
                    Text(shortcut)
                        .font(.caption.monospaced().weight(.medium))
                        .foregroundStyle(isSelected ? .primary : .secondary)
                }
            }
            .padding(.horizontal, Spacing.md)
            .padding(.vertical, Spacing.sm)
            .background {
                RoundedRectangle(cornerRadius: Radius.chip, style: .continuous)
                    .fill(isSelected ? Color.accentColor.opacity(0.35) : Surface.chip)
            }
            .overlay {
                RoundedRectangle(cornerRadius: Radius.chip, style: .continuous)
                    .stroke(
                        isSelected ? Color.accentColor : Surface.chipBorder,
                        lineWidth: 1
                    )
            }
        }
        .buttonStyle(.plain)
        .disabled(!agent.available)
        .opacity(agent.available ? 1 : Dimming.unavailable)
        .accessibilityLabel(agentAccessibilityLabel(agent, index: index))
        .accessibilityAddTraits(isSelected ? .isSelected : [])
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
        appModel.threadList(filter: .all).projects
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
        workingDirectory =
            rows.first { $0.id == ref }?
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
            let thread = try await runtime.threads.create(
                .init(
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

// The single sidebar: Dashboard, New Thread, pinned threads, the thread
// filter, the filtered thread list, and the contextual Terminals section —
// one scroll view, in that order.
//
// Two invariants the layout does not reveal: the filter governs the thread
// list ONLY (pins, Dashboard, New Thread and Terminals are global), and the
// filtered thread list collapses back to `initialRowLimit` when the filter
// changes, so a deep-scrolled list never carries over into an unrelated one
// (pins are filter-independent and keep their expansion).
//
// Mutations are never optimistic. A row whose mutation is in flight keeps its
// place with its controls disabled (`inFlightThreads`); the SSE-driven
// refresh is what reorders it.

import ATCAppServerAPI
import SwiftUI

struct NavigatorSidebar: View {
    @Environment(AppModel.self) private var appModel
    @Environment(WindowState.self) private var windowState
    @Environment(WindowKeyboardRouter.self) private var router

    @State private var renamingThread: ThreadListItem?
    @State private var renamingTerminal: TerminalRef?
    @State private var renameDraft = ""
    @State private var deletingThread: ThreadListItem?
    @State private var deletingTerminal: TerminalRef?
    @State private var inFlightThreads: Set<ThreadRef> = []
    @State private var actionError: String?
    @FocusState private var focusedThread: ThreadRef?

    var body: some View {
        @Bindable var windowState = windowState
        let model = ThreadListModel(
            inputs: appModel.runtimes.map(ThreadListModel.ConnectionInput.init(runtime:)),
            filter: windowState.threadFilter
        )
        let threadSlots = threadSlotNumbers(model)
        NavigatorList {
            headerRow

            if !model.pinned.isEmpty {
                threadCards(
                    model.pinned,
                    isExpanded: $windowState.isPinnedExpanded,
                    slotNumbers: threadSlots
                )
                sectionDivider
            }

            filterRow(model)
            threadListSection(model, slotNumbers: threadSlots)

            if let project = windowState.activeProject {
                sectionDivider
                terminalsSection(project)
            }
        }
        .navigationSplitViewColumnWidth(min: 240, ideal: 300)
        .renameAlert("Rename Thread", item: $renamingThread, draft: $renameDraft) { item, name in
            mutateThread(item.ref) { try await $0.rename(id: item.ref.threadID, name: name) }
        }
        .renameAlert("Rename Terminal", item: $renamingTerminal, draft: $renameDraft) { ref, name in
            if let store = appModel.runtime(id: ref.connectionID)?.terminals {
                appModel.run(on: ref.connectionID, reporting: $actionError) {
                    try await store.rename(id: ref.terminalID, name: name)
                }
            }
        }
        .confirmationDialog(
            "Delete Thread “\(deletingThread?.thread.displayName ?? "")”?",
            isPresented: Binding(presented: $deletingThread)
        ) {
            Button("Delete Thread", role: .destructive) {
                if let item = deletingThread {
                    mutateThread(item.ref) { try await $0.delete(id: item.ref.threadID) }
                }
            }
        } message: {
            Text("""
            This removes the thread from atc and ends its TUI terminal if one is \
            still running. The agent's own conversation history is not touched.
            """)
        }
        .confirmationDialog(
            "Delete Terminal “\(deletingTerminalName)”?",
            isPresented: Binding(presented: $deletingTerminal)
        ) {
            Button("Delete Terminal", role: .destructive) {
                if let ref = deletingTerminal,
                   let store = appModel.runtime(id: ref.connectionID)?.terminals {
                    appModel.run(on: ref.connectionID, reporting: $actionError) {
                        try await store.delete(id: ref.terminalID)
                    }
                }
            }
        } message: {
            Text("A live terminal is ended, and its scrollback is discarded.")
        }
        .actionErrorAlert($actionError)
    }

    // MARK: - Fixed rows

    /// Dashboard plus the compose (New Thread) chip in one header row, per
    /// the design: the chip is the dedicated New Thread action.
    private var headerRow: some View {
        HStack(spacing: Spacing.sm) {
            NavigatorRow(
                isSelected: windowState.selectedContent == .dashboard,
                action: { windowState.showDashboard() }
            ) { _ in
                NavigatorIconLabel(title: "Dashboard", systemImage: "square.grid.2x2")
            } actions: {
                EmptyView()
            }

            NavigatorChipButton(
                systemImage: "square.and.pencil",
                help: "New Thread",
                isEnabled: canCreateAnywhere
            ) {
                windowState.presentNewThread(in: appModel)
            }
        }
        .navigatorListRow(top: Spacing.sm, bottom: Spacing.sm)
    }

    private func filterRow(_ model: ThreadListModel) -> some View {
        HStack(spacing: Spacing.sm) {
            NavigatorDropdown(entries: filterEntries(model)) {
                HStack(spacing: Spacing.sm) {
                    Image(systemName: "folder")
                        .foregroundStyle(.secondary)
                    Text(filterTitle(model))
                        .font(.callout.weight(.medium))
                        .lineLimit(1)
                    Spacer(minLength: Spacing.xs)
                    Image(systemName: "chevron.down")
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                }
                .padding(.horizontal, Spacing.md)
                .frame(maxWidth: .infinity, minHeight: NavigatorMetrics.chipSize)
            }
            .help("Filter threads")
            .accessibilityLabel("Thread filter: \(filterTitle(model))")

            NavigatorChipButton(
                systemImage: "plus",
                help: "New project",
                isEnabled: canCreateAnywhere
            ) {
                windowState.isCreateProjectPresented = true
            }
        }
        // Flush with the card edges — the chips share the cards' gutter.
        .navigatorListRow(top: Spacing.sm, bottom: Spacing.sm)
    }

    // MARK: - Thread list

    @ViewBuilder
    private func threadListSection(
        _ model: ThreadListModel,
        slotNumbers: [ThreadRef: Int]
    ) -> some View {
        @Bindable var windowState = windowState
        if case .archived = windowState.threadFilter {
            if model.archived.isEmpty {
                emptyLabel("No Archived Threads")
            } else {
                ForEach(limited(model.archived, expanded: windowState.isArchivedExpanded)) { item in
                    archivedRow(item)
                }
                moreControl(total: model.archived.count, isExpanded: $windowState.isArchivedExpanded)
            }
        } else if model.recent.isEmpty {
            emptyLabel("No Threads")
        } else {
            threadCards(
                model.recent,
                isExpanded: $windowState.isRecentExpanded,
                slotNumbers: slotNumbers
            )
        }
    }

    @ViewBuilder
    private func threadCards(
        _ items: [ThreadListItem],
        isExpanded: Binding<Bool>,
        slotNumbers: [ThreadRef: Int]
    ) -> some View {
        ForEach(limited(items, expanded: isExpanded.wrappedValue)) { item in
            ThreadCard(
                item: item,
                reachability: appModel.runtime(id: item.ref.connectionID)?.reachability ?? .unknown,
                isSelected: windowState.isThreadHighlighted(item.ref),
                isBusy: inFlightThreads.contains(item.ref),
                canMutate: appModel.canMutate(connectionID: item.ref.connectionID),
                shortcutLabel: slotNumbers[item.ref].map(SidebarShortcuts.threadBadgeLabel),
                focusedThread: $focusedThread,
                onOpen: { Task { await windowState.openThread(item.ref, in: appModel) } },
                onRename: {
                    renameDraft = item.thread.name ?? ""
                    renamingThread = item
                },
                onTogglePin: {
                    mutateThread(item.ref) { store in
                        if item.thread.isPinned {
                            try await store.unpin(id: item.ref.threadID)
                        } else {
                            try await store.pin(id: item.ref.threadID)
                        }
                    }
                },
                onArchive: {
                    mutateThread(item.ref) { try await $0.archive(id: item.ref.threadID) }
                }
            )
        }
        moreControl(total: items.count, isExpanded: isExpanded)
    }

    /// Archived threads are management rows, not navigation: Restore returns
    /// the thread to the list and opens it, Delete is the only other action.
    private func archivedRow(_ item: ThreadListItem) -> some View {
        let enabled = appModel.canMutate(connectionID: item.ref.connectionID)
            && !inFlightThreads.contains(item.ref)
        return HStack(spacing: Spacing.sm) {
            Text("\(item.projectLabel) › \(item.thread.displayName)")
                .font(.callout)
                .lineLimit(1)
                .truncationMode(.middle)
            Spacer(minLength: Spacing.sm)
            Button {
                restore(item)
            } label: {
                Image(systemName: "arrow.uturn.backward")
                    .frame(width: NavigatorMetrics.actionSize, height: NavigatorMetrics.actionSize)
            }
            .buttonStyle(.plain)
            .disabled(!enabled)
            .help("Restore and open")
            .accessibilityLabel("Restore \(item.thread.displayName)")

            Button {
                deletingThread = item
            } label: {
                Image(systemName: "trash")
                    .frame(width: NavigatorMetrics.actionSize, height: NavigatorMetrics.actionSize)
            }
            .buttonStyle(.plain)
            .disabled(!enabled)
            .help("Delete thread")
            .accessibilityLabel("Delete \(item.thread.displayName)")
        }
        .foregroundStyle(.secondary)
        .padding(.horizontal, NavigatorMetrics.contentHorizontalPadding)
        .frame(minHeight: NavigatorMetrics.rowHeight)
        .navigatorListRow()
    }

    // MARK: - Terminals

    @ViewBuilder
    private func terminalsSection(_ project: ProjectRef) -> some View {
        @Bindable var windowState = windowState
        let terminals = appModel.runtime(id: project.connectionID)?
            .terminals.standaloneTerminals(projectID: project.projectID) ?? []
        NavigatorDisclosureHeader(
            title: "Terminals",
            isExpanded: $windowState.isTerminalsSectionExpanded,
            addHelp: "New terminal",
            isAddEnabled: appModel.canMutate(connectionID: project.connectionID),
            onAdd: { windowState.newTerminalProject = project }
        )
        if windowState.isTerminalsSectionExpanded {
            if terminals.isEmpty {
                emptyLabel("No Terminals")
            } else {
                let numbered = areTerminalBadgesVisible
                    ? SidebarShortcuts.terminalTargets(terminals, isSectionExpanded: true).count
                    : 0
                ForEach(Array(terminals.enumerated()), id: \.element.id) { index, terminal in
                    terminalRow(
                        terminal,
                        connectionID: project.connectionID,
                        shortcutLabel: index < numbered
                            ? SidebarShortcuts.terminalBadgeLabel(index + 1)
                            : nil
                    )
                }
            }
        }
    }

    private func terminalRow(
        _ terminal: Terminal,
        connectionID: UUID,
        shortcutLabel: String?
    ) -> some View {
        let ref = TerminalRef(connectionID: connectionID, terminalID: terminal.id)
        return NavigatorRow(
            isSelected: windowState.selectedContent == .terminal(ref),
            action: { windowState.selectTerminal(ref, in: appModel) }
        ) { _ in
            HStack(spacing: Spacing.sm) {
                Image(systemName: "terminal")
                    .frame(width: NavigatorMetrics.iconWidth, alignment: .leading)
                    .foregroundStyle(.secondary)
                Text(terminal.displayName)
                    .lineLimit(1)
                if !terminal.isLive {
                    Text("Ended")
                        .font(.caption)
                        .foregroundStyle(.tertiary)
                }
                if let shortcutLabel {
                    Spacer(minLength: Spacing.sm)
                    ShortcutBadge(label: shortcutLabel)
                }
            }
        } actions: {
            EmptyView()
        }
        .contextMenu {
            Button("Rename…", systemImage: "pencil") {
                renameDraft = terminal.name ?? ""
                renamingTerminal = ref
            }
            .disabled(!appModel.canMutate(connectionID: connectionID))
            Divider()
            Button("Delete Terminal…", systemImage: "trash", role: .destructive) {
                deletingTerminal = ref
            }
            .disabled(!appModel.canMutate(connectionID: connectionID))
        }
    }

    // MARK: - Shared row chrome

    private var sectionDivider: some View {
        Divider()
            .padding(.horizontal, NavigatorMetrics.contentHorizontalPadding)
            .navigatorListRow(top: Spacing.sm, bottom: Spacing.sm)
    }

    private func emptyLabel(_ title: String) -> some View {
        Text(title)
            .font(.caption)
            .foregroundStyle(.tertiary)
            .padding(.horizontal, NavigatorMetrics.contentHorizontalPadding)
            .frame(minHeight: NavigatorMetrics.rowHeight, alignment: .leading)
            .navigatorListRow()
    }

    @ViewBuilder
    private func moreControl(total: Int, isExpanded: Binding<Bool>) -> some View {
        if total > SidebarShortcuts.initialRowLimit {
            Button {
                isExpanded.wrappedValue.toggle()
            } label: {
                HStack(spacing: Spacing.xs) {
                    Text(isExpanded.wrappedValue
                        ? "Hide"
                        : "\(total - SidebarShortcuts.initialRowLimit) more")
                    Image(systemName: isExpanded.wrappedValue ? "chevron.up" : "chevron.down")
                        .font(.caption2)
                }
                .font(.caption.weight(.medium))
                .frame(maxWidth: .infinity)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .foregroundStyle(.secondary)
            .padding(.horizontal, NavigatorMetrics.contentHorizontalPadding)
            .frame(minHeight: NavigatorMetrics.rowHeight)
            .navigatorListRow()
        }
    }

    // MARK: - Actions

    /// Every filter change resets both list limits: a "More" state belongs to
    /// the list it was opened on, never to whatever replaces it.
    private func select(_ filter: ThreadFilter) {
        windowState.threadFilter = filter
        windowState.isRecentExpanded = false
        windowState.isArchivedExpanded = false
        guard case .archived = filter else { return }
        for runtime in appModel.runtimes {
            Task { await runtime.threads.loadArchivedIfNeeded() }
        }
    }

    private func restore(_ item: ThreadListItem) {
        mutateThread(item.ref) { store in
            try await store.unarchive(id: item.ref.threadID)
            await windowState.openThread(item.ref, in: appModel)
        }
    }

    /// Owns the in-flight mark outside the operation: a refused mutation
    /// (unreachable Connection) must release the row, not freeze it.
    private func mutateThread(
        _ ref: ThreadRef,
        _ operation: @escaping (ThreadsStore) async throws -> Void
    ) {
        guard let store = appModel.runtime(id: ref.connectionID)?.threads else { return }
        guard appModel.canMutate(connectionID: ref.connectionID) else {
            actionError = "The connection is unavailable."
            return
        }
        inFlightThreads.insert(ref)
        Task {
            defer { inFlightThreads.remove(ref) }
            do {
                try await operation(store)
            } catch {
                actionError = error.localizedDescription
            }
        }
    }

    // MARK: - Derived state

    private var canCreateAnywhere: Bool {
        appModel.runtimes.contains { appModel.canMutate(connectionID: $0.id) }
    }

    private var deletingTerminalName: String {
        deletingTerminal.flatMap { appModel.terminal(for: $0)?.displayName } ?? ""
    }

    private func filterTitle(_ model: ThreadListModel) -> String {
        switch windowState.threadFilter {
        case .all: "All Projects"
        case .archived: "Archived"
        case .project(let ref):
            model.projects.first { $0.ref == ref }?.label ?? "Project"
        }
    }

    private func filterEntries(_ model: ThreadListModel) -> [NavigatorDropdownEntry] {
        var entries: [NavigatorDropdownEntry] = [
            .item(
                title: "All Projects",
                isSelected: windowState.threadFilter == .all,
                action: { select(.all) }
            ),
        ]
        if !model.projects.isEmpty {
            entries.append(.separator)
            for option in model.projects {
                entries.append(.item(
                    title: option.label,
                    isSelected: windowState.threadFilter == .project(option.ref),
                    action: { select(.project(option.ref)) }
                ))
            }
        }
        entries.append(.separator)
        entries.append(.item(
            title: "Archived",
            isSelected: windowState.threadFilter == .archived,
            action: { select(.archived) }
        ))
        return entries
    }

    private func limited<T>(_ items: [T], expanded: Bool) -> [T] {
        SidebarShortcuts.limited(items, expanded: expanded)
    }

    // MARK: - Jump shortcut badges

    /// Exact modifier match only: ⌘ alone lights thread badges, ⌥⌘ lights
    /// terminal badges, any other combination shows neither. The palette
    /// suspends dispatch, so it suppresses the badges too.
    private var areThreadBadgesVisible: Bool {
        router.heldModifiers == [.command]
            && windowState.commandPalettePresentation == nil
    }

    private var areTerminalBadgesVisible: Bool {
        router.heldModifiers == [.command, .option]
            && windowState.commandPalettePresentation == nil
    }

    private func threadSlotNumbers(_ model: ThreadListModel) -> [ThreadRef: Int] {
        guard areThreadBadgesVisible else { return [:] }
        let targets = SidebarShortcuts.threadTargets(
            model: model,
            isPinnedExpanded: windowState.isPinnedExpanded,
            isRecentExpanded: windowState.isRecentExpanded
        )
        return Dictionary(uniqueKeysWithValues: targets.enumerated().map {
            ($0.element, $0.offset + 1)
        })
    }
}

/// The ⌘N / ⌥⌘N chip shown while the exact modifier combo is held. Matches
/// the card action chips' height so swapping it in never resizes a row.
private struct ShortcutBadge: View {
    let label: String

    var body: some View {
        Text(label)
            .font(.caption.weight(.semibold))
            .foregroundStyle(.secondary)
            .padding(.horizontal, Spacing.xs)
            .frame(height: NavigatorMetrics.actionSize)
            .background(
                Color.white.opacity(0.07),
                in: RoundedRectangle(cornerRadius: Radius.control, style: .continuous)
            )
    }
}

/// One thread in the sidebar. The whole card opens the thread; Pin and
/// Archive stay out of sight until hover or keyboard focus, and remain
/// independently clickable and tabbable when shown.
private struct ThreadCard: View {
    let item: ThreadListItem
    let reachability: Reachability
    let isSelected: Bool
    let isBusy: Bool
    let canMutate: Bool
    /// Non-nil while exact ⌘ is held and this card holds a numbered slot.
    let shortcutLabel: String?
    @FocusState.Binding var focusedThread: ThreadRef?
    let onOpen: () -> Void
    let onRename: () -> Void
    let onTogglePin: () -> Void
    let onArchive: () -> Void

    @State private var isHovering = false

    private var isFocused: Bool { focusedThread == item.ref }

    var body: some View {
        let thread = item.thread
        VStack(alignment: .leading, spacing: Spacing.xs) {
            // Top row: Project name as compact context; the trailing area
            // shows the Connection at rest and the actions on hover/focus.
            HStack(alignment: .center, spacing: Spacing.sm) {
                Text(item.projectLabel)
                    .font(.caption.weight(.semibold))
                    .lineLimit(1)
                Spacer(minLength: Spacing.sm)
                // Same slot as the hover chips and the connection status, so
                // the card never resizes; the held-modifier badge wins.
                if let shortcutLabel {
                    ShortcutBadge(label: shortcutLabel)
                } else if isHovering || isFocused {
                    HStack(spacing: Spacing.xs) {
                        cardAction(
                            systemImage: thread.isPinned ? "pin.slash" : "pin",
                            help: thread.isPinned ? "Unpin" : "Pin",
                            isEnabled: canMutate && !isBusy,
                            action: onTogglePin
                        )
                        cardAction(
                            systemImage: "archivebox",
                            help: "Archive",
                            // The server refuses archiving a working thread;
                            // the control is disabled rather than surfacing
                            // that as an error the user did not ask for.
                            isEnabled: canMutate && !isBusy
                                && thread.activityState != .working,
                            action: onArchive
                        )
                    }
                } else {
                    HStack(spacing: Spacing.xs) {
                        Text(item.connectionName)
                            .font(.caption)
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                        StatusDot(reachability: reachability, size: .inline)
                    }
                    // Match the action chips' height so the card does not
                    // resize under the pointer.
                    .frame(height: NavigatorMetrics.actionSize)
                }
            }

            // The dominant element.
            Text(thread.displayName)
                .font(.body.weight(.medium))
                .lineLimit(2)
                .multilineTextAlignment(.leading)

            // Bottom metadata row: working directory when it differs from
            // the Project default; activity state and Agent trailing.
            HStack(alignment: .center, spacing: Spacing.sm) {
                if let directory = item.distinctWorkingDirectory {
                    Text(directory)
                        .font(.caption.monospaced())
                        .foregroundStyle(.tertiary)
                        .lineLimit(1)
                        .truncationMode(.head)
                }
                Spacer(minLength: Spacing.sm)
                if let status = thread.activityState.statusLabel {
                    Text(status)
                        .font(.caption.weight(.medium))
                        .foregroundStyle(thread.activityState.statusColor)
                }
                agentBadge
            }
        }
        .padding(Spacing.md)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background {
            RoundedRectangle(cornerRadius: Radius.card, style: .continuous)
                .fill(backgroundStyle)
        }
        .overlay {
            RoundedRectangle(cornerRadius: Radius.card, style: .continuous)
                .strokeBorder(borderColor, lineWidth: 1)
        }
        .opacity(isBusy ? Dimming.unavailable : 1)
        .contentShape(Rectangle())
        .onHover { isHovering = $0 }
        .focusable()
        .focusEffectDisabled()
        .focused($focusedThread, equals: item.ref)
        .onTapGesture {
            focusedThread = item.ref
            onOpen()
        }
        .onKeyPress(.return) {
            onOpen()
            return .handled
        }
        .contextMenu {
            Button("Rename…", systemImage: "pencil", action: onRename)
                .disabled(!canMutate || isBusy)
            Button(
                thread.isPinned ? "Unpin" : "Pin",
                systemImage: thread.isPinned ? "pin.slash" : "pin",
                action: onTogglePin
            )
            .disabled(!canMutate || isBusy)
            Button("Archive", systemImage: "archivebox", action: onArchive)
                .disabled(!canMutate || isBusy || thread.activityState == .working)
        }
        .accessibilityElement(children: .contain)
        .accessibilityAddTraits(isSelected ? [.isButton, .isSelected] : .isButton)
        .accessibilityAction(named: "Open", onOpen)
        .navigatorListRow(top: Spacing.xs, bottom: Spacing.xs)
    }

    /// The Agent icon in a small rounded-square badge, lower-trailing per
    /// the design.
    private var agentBadge: some View {
        Image(systemName: item.thread.agentId.systemImage)
            .font(.caption2)
            .foregroundStyle(.secondary)
            .frame(width: NavigatorMetrics.actionSize, height: NavigatorMetrics.actionSize)
            .background(
                Color.white.opacity(0.07),
                in: RoundedRectangle(cornerRadius: Radius.control, style: .continuous)
            )
            .accessibilityLabel(item.thread.agentId.displayName)
    }

    private func cardAction(
        systemImage: String,
        help: String,
        isEnabled: Bool,
        action: @escaping () -> Void
    ) -> some View {
        Button(action: action) {
            Image(systemName: systemImage)
                .font(.caption)
                .frame(width: NavigatorMetrics.actionSize, height: NavigatorMetrics.actionSize)
                .background(
                    Color.white.opacity(0.07),
                    in: RoundedRectangle(cornerRadius: Radius.control, style: .continuous)
                )
                .contentShape(RoundedRectangle(cornerRadius: Radius.control, style: .continuous))
        }
        .buttonStyle(.plain)
        .foregroundStyle(.secondary)
        .disabled(!isEnabled)
        .help(help)
        .accessibilityLabel(help)
    }

    /// Cards read as bordered containers at rest; selection is the accent
    /// border plus tint, hover a brighter neutral fill.
    private var backgroundStyle: AnyShapeStyle {
        if isSelected { return AnyShapeStyle(Color.accentColor.opacity(0.13)) }
        if isHovering { return AnyShapeStyle(Color.white.opacity(0.07)) }
        return AnyShapeStyle(Color.white.opacity(0.035))
    }

    private var borderColor: Color {
        if isSelected || isFocused { return Color.accentColor }
        return Color.white.opacity(0.08)
    }
}

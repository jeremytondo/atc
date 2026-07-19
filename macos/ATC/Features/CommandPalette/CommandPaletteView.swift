import AppKit
import SwiftUI

struct CommandPaletteView: View {
    @Environment(AppModel.self) private var appModel
    @Environment(WindowState.self) private var windowState
    @Environment(ConfigurationStore.self) private var configStore
    @Environment(WindowKeyboardRouter.self) private var router

    @State private var query = ""
    @State private var selectedID: PaletteResultID?
    @State private var hoveredID: PaletteResultID?
    @State private var responderRestoration = PaletteResponderRestoration()
    @FocusState private var queryIsFocused: Bool

    init(initialQuery: String = "") {
        _query = State(initialValue: initialQuery)
    }

    var body: some View {
        let presentation = windowState.commandPalettePresentation ?? .all
        let context = CommandContext(
            appModel: appModel,
            windowState: windowState,
            configStore: configStore
        )
        let rows = CommandPaletteContent.results(
            query: query,
            keymap: configStore.configuration.keymap,
            context: context,
            presentation: presentation
        )

        ZStack(alignment: .top) {
            Color.clear
                .contentShape(Rectangle())
                .onTapGesture { dismissPalette() }

            palettePanel(rows: rows, context: context, presentation: presentation)
                .frame(maxWidth: 500)
                .padding(.horizontal, 20)
                .padding(.top, 48)
        }
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Command Palette")
        .accessibilityAddTraits(.isModal)
        .onExitCommand { dismissPalette() }
        .background(PaletteWindowAccessor(
            takeCapturedResponder: {
                defer { router.responderBeforeSuspension = nil }
                return router.responderBeforeSuspension as? NSResponder
            },
            onAttach: { queryIsFocused = true },
            shouldRestoreCapturedResponder: {
                responderRestoration.shouldRestoreCapturedResponder
            },
            fallback: { windowState.requestTerminalFocus() }
        ))
        .onAppear { resetSelection(for: rows, presentation: presentation) }
        .onChange(of: query) {
            resetSelection(for: rows, presentation: presentation)
            let trimmed = query.trimmingCharacters(in: .whitespacesAndNewlines)
            if !trimmed.isEmpty, rows.isEmpty {
                AccessibilityNotification.Announcement("No matching results").post()
            }
        }
        // Store refreshes can change the rows without a query change; a
        // selection that no longer resolves falls back to the standard rule.
        .onChange(of: rows.map(\.id)) {
            if let selectedID, rows.contains(where: { $0.id == selectedID }) { return }
            resetSelection(for: rows, presentation: presentation)
        }
        // Keyboard focus stays on the query field while arrows move the
        // selection, so VoiceOver never lands on the rows; announce the
        // active row instead.
        .onChange(of: selectedID) {
            guard let selectedID,
                  let result = rows.first(where: { $0.id == selectedID })
            else { return }
            AccessibilityNotification.Announcement(accessibilityLabel(for: result)).post()
        }
    }

    private func palettePanel(
        rows: [PaletteResult],
        context: CommandContext,
        presentation: CommandPalettePresentation
    ) -> some View {
        VStack(spacing: 0) {
            TextField(placeholder(for: presentation), text: $query)
                .textFieldStyle(.plain)
                .font(.title3)
                .padding(.horizontal, 14)
                .padding(.vertical, 12)
                .focused($queryIsFocused)
                .accessibilityLabel("Palette search")
                .onSubmit { activateSelection(in: rows, context: context) }
                .onKeyPress(.downArrow) { moveSelection(1, through: rows) }
                .onKeyPress(.upArrow) { moveSelection(-1, through: rows) }
                .onKeyPress(keys: ["n"], phases: .down) { press in
                    guard press.modifiers == .control else { return .ignored }
                    return moveSelection(1, through: rows)
                }
                .onKeyPress(keys: ["p"], phases: .down) { press in
                    guard press.modifiers == .control else { return .ignored }
                    return moveSelection(-1, through: rows)
                }
                .onKeyPress(.escape) {
                    dismissPalette()
                    return .handled
                }

            Divider()

            resultList(rows: rows, context: context, presentation: presentation)
        }
        .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 11))
        .overlay {
            RoundedRectangle(cornerRadius: 11)
                .stroke(Color(nsColor: .separatorColor).opacity(0.7), lineWidth: 0.5)
        }
        .shadow(color: .black.opacity(0.28), radius: 18, y: 7)
    }

    @ViewBuilder
    private func resultList(
        rows: [PaletteResult],
        context: CommandContext,
        presentation: CommandPalettePresentation
    ) -> some View {
        if rows.isEmpty {
            Text(emptyStateText(for: presentation))
                .font(.callout)
                .foregroundStyle(.secondary)
                .frame(maxWidth: .infinity, minHeight: 44)
        } else {
            ScrollViewReader { proxy in
                ScrollView {
                    LazyVStack(spacing: 2) {
                        ForEach(rows) { result in
                            resultRow(result, context: context)
                                .id(result.id)
                        }
                    }
                    .padding(5)
                }
                .frame(height: min(200, rows.reduce(CGFloat(10)) {
                    $0 + estimatedRowHeight(for: $1)
                }))
                .onChange(of: selectedID) {
                    if let selectedID {
                        proxy.scrollTo(selectedID, anchor: .center)
                    }
                }
            }
        }
    }

    /// Shrink-to-fit estimates only: workspace rows carry a context caption
    /// and unavailable rows an inline reason line, so a short list is not
    /// initially clipped. The 200 pt cap and the scroll view absorb any
    /// estimate error on longer lists.
    private func estimatedRowHeight(for result: PaletteResult) -> CGFloat {
        switch result {
        case .command(let row):
            row.availability.isAvailable ? 42 : 56
        case .workspace(let row):
            row.availability.isAvailable ? 56 : 70
        case .session:
            42
        }
    }

    private func resultRow(
        _ result: PaletteResult,
        context: CommandContext
    ) -> some View {
        let isSelected = selectedID == result.id
        let isHovered = hoveredID == result.id
        let isAvailable = availability(for: result).isAvailable
        return HStack(spacing: 10) {
            resultContent(result)
        }
        .padding(.horizontal, 10)
        .padding(.vertical, 7)
        .foregroundStyle(isAvailable ? .primary : .secondary)
        .opacity(isAvailable ? 1 : 0.6)
        .background {
            RoundedRectangle(cornerRadius: 6)
                .fill(isSelected ? Color.accentColor.opacity(0.65) :
                    isHovered ? Color.primary.opacity(0.08) : .clear)
        }
        .contentShape(Rectangle())
        .onHover { hovering in hoveredID = hovering ? result.id : nil }
        .onTapGesture {
            selectedID = result.id
            activate(result, context: context)
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(accessibilityLabel(for: result))
        .accessibilityAddTraits(isSelected ? .isSelected : [])
        .accessibilityAction { activate(result, context: context) }
    }

    @ViewBuilder
    private func resultContent(_ result: PaletteResult) -> some View {
        switch result {
        case .command(let row):
            VStack(alignment: .leading, spacing: 2) {
                highlightedTitle(row.title, ranges: row.matchedRanges)
                    .font(.callout)
                unavailableReason(row.availability)
            }
            Spacer(minLength: 12)
            if let shortcut = row.shortcut {
                trailingLabel(shortcut.displayDescription)
            }
        case .workspace(let row):
            VStack(alignment: .leading, spacing: 2) {
                typedTitle(
                    "Workspace",
                    title: row.title,
                    ranges: row.matchedRanges
                )
                    .font(.callout)
                Text("\(row.projectName) · \(row.connectionName)")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                unavailableReason(row.availability)
            }
            Spacer(minLength: 12)
        case .session(let row):
            typedTitle(
                row.kind.paletteTypeLabel,
                title: row.title,
                ranges: row.matchedRanges
            )
                .font(.callout)
            Spacer(minLength: 12)
        }
    }

    /// One flowing Text so a long name wraps as a unit with its prefix. The
    /// prefix is dropped when the name already is the type label — an unnamed
    /// shell reads "Terminal", not "Terminal: Terminal".
    private func typedTitle(
        _ type: String,
        title: String,
        ranges: [Range<String.Index>]
    ) -> Text {
        let name = highlightedTitle(title, ranges: ranges)
        guard title != type else { return name }
        return Text("\(type): ").foregroundStyle(.secondary) + name
    }

    @ViewBuilder
    private func unavailableReason(_ availability: CommandAvailability) -> some View {
        if case .unavailable(let reason) = availability {
            Text(reason)
                .font(.caption2)
                .foregroundStyle(.tertiary)
        }
    }

    private func trailingLabel(_ label: String) -> some View {
        Text(label)
            .font(.caption.monospaced().weight(.medium))
            .foregroundStyle(.secondary)
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

    private func accessibilityLabel(for result: PaletteResult) -> String {
        var parts: [String]
        switch result {
        case .command(let row):
            parts = [row.title]
            if case .unavailable(let reason) = row.availability {
                parts.append("Unavailable — \(reason)")
            }
            if let shortcut = row.shortcut {
                parts.append(shortcut.spokenDescription)
            }
        case .workspace(let row):
            parts = [row.title, "Workspace", row.projectName, row.connectionName]
            if case .unavailable(let reason) = row.availability {
                parts.append("Unavailable — \(reason)")
            }
        case .session(let row):
            parts = [row.title]
            if row.title != row.kind.paletteTypeLabel {
                parts.append(row.kind.paletteTypeLabel)
            }
        }
        return parts.joined(separator: ", ")
    }

    private func availability(for result: PaletteResult) -> CommandAvailability {
        switch result {
        case .command(let row): row.availability
        case .workspace(let row): row.availability
        case .session: .available
        }
    }

    private func activateSelection(
        in rows: [PaletteResult],
        context: CommandContext
    ) {
        guard let selectedID,
              let result = rows.first(where: { $0.id == selectedID })
        else {
            dismissPalette()
            return
        }
        activate(result, context: context)
    }

    private func activate(_ result: PaletteResult, context: CommandContext) {
        switch result {
        case .command(let row):
            guard row.availability.isAvailable else {
                if case .unavailable(let reason) = row.availability {
                    router.showUnavailable(reason: reason)
                }
                return
            }
            dismissPalette()
            CommandRegistry.execute(row.id, context: context)
        case .workspace(let row):
            guard row.availability.isAvailable else {
                if case .unavailable(let reason) = row.availability {
                    router.showUnavailable(reason: reason)
                }
                return
            }
            // Dismiss only on success: a target that vanished between
            // projection and activation fails closed in WindowState, and the
            // recomputed rows drop it in the same turn.
            if windowState.activateWorkspace(row.ref, in: appModel) {
                dismissForNavigation()
            }
        case .session(let row):
            if windowState.selectSession(row.ref, in: appModel) {
                dismissForNavigation()
            }
        }
    }

    private func dismissForNavigation() {
        responderRestoration.shouldRestoreCapturedResponder = false
        dismissPalette()
    }

    private func dismissPalette() {
        windowState.commandPalettePresentation = nil
    }

    /// Scoped palettes always select the first row; the unscoped palette does
    /// so only for a nonempty query. Applied on appearance too, so a palette
    /// hosted with a prefilled query starts with a live selection.
    private func resetSelection(
        for rows: [PaletteResult],
        presentation: CommandPalettePresentation
    ) {
        let trimmed = query.trimmingCharacters(in: .whitespacesAndNewlines)
        selectedID = presentation == .all && trimmed.isEmpty ? nil : rows.first?.id
    }

    private func placeholder(for presentation: CommandPalettePresentation) -> String {
        switch presentation {
        case .all: "Search commands and navigation…"
        case .sessions: "Search Sessions…"
        case .terminals: "Search Terminals…"
        case .workspaces: "Search Workspaces…"
        }
    }

    private func emptyStateText(for presentation: CommandPalettePresentation) -> String {
        guard query.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else {
            return "No matching results"
        }
        return switch presentation {
        case .all: "No matching results"
        case .sessions: "No Sessions"
        case .terminals: "No Terminals"
        case .workspaces: "No Workspaces"
        }
    }

    private func moveSelection(
        _ offset: Int,
        through rows: [PaletteResult]
    ) -> KeyPress.Result {
        guard !rows.isEmpty else { return .handled }
        guard let selectedID,
              let index = rows.firstIndex(where: { $0.id == selectedID })
        else {
            self.selectedID = offset > 0 ? rows.first?.id : rows.last?.id
            return .handled
        }
        self.selectedID = rows[(index + offset + rows.count) % rows.count].id
        return .handled
    }
}

private extension SessionKind {
    /// The one palette type label, shared by the row prefix and VoiceOver.
    var paletteTypeLabel: String {
        switch self {
        case .agent: "Session"
        case .terminal: "Terminal"
        }
    }
}

private final class PaletteResponderRestoration {
    var shouldRestoreCapturedResponder = true
}

private struct PaletteWindowAccessor: NSViewRepresentable {
    let takeCapturedResponder: @MainActor () -> NSResponder?
    let onAttach: @MainActor () -> Void
    let shouldRestoreCapturedResponder: @MainActor () -> Bool
    let fallback: @MainActor () -> Void

    func makeCoordinator() -> Coordinator {
        Coordinator(
            takeCapturedResponder: takeCapturedResponder,
            onAttach: onAttach,
            shouldRestoreCapturedResponder: shouldRestoreCapturedResponder,
            fallback: fallback
        )
    }

    func makeNSView(context: Context) -> HostView {
        let view = HostView()
        view.onWindowChange = { [weak coordinator = context.coordinator] window in
            coordinator?.attach(to: window)
        }
        return view
    }

    func updateNSView(_ nsView: HostView, context: Context) {
        context.coordinator.takeCapturedResponder = takeCapturedResponder
        context.coordinator.onAttach = onAttach
        context.coordinator.shouldRestoreCapturedResponder = shouldRestoreCapturedResponder
        context.coordinator.fallback = fallback
        if nsView.window !== context.coordinator.hostWindow {
            context.coordinator.attach(to: nsView.window)
        }
    }

    static func dismantleNSView(_ nsView: HostView, coordinator: Coordinator) {
        nsView.onWindowChange = nil
        coordinator.restore()
    }

    final class HostView: NSView {
        var onWindowChange: ((NSWindow?) -> Void)?

        override func viewDidMoveToWindow() {
            super.viewDidMoveToWindow()
            onWindowChange?(window)
        }
    }

    @MainActor
    final class Coordinator {
        var takeCapturedResponder: @MainActor () -> NSResponder?
        var onAttach: @MainActor () -> Void
        var shouldRestoreCapturedResponder: @MainActor () -> Bool
        var fallback: @MainActor () -> Void
        private(set) weak var hostWindow: NSWindow?
        private weak var previousResponder: NSResponder?

        init(
            takeCapturedResponder: @escaping @MainActor () -> NSResponder?,
            onAttach: @escaping @MainActor () -> Void,
            shouldRestoreCapturedResponder: @escaping @MainActor () -> Bool,
            fallback: @escaping @MainActor () -> Void
        ) {
            self.takeCapturedResponder = takeCapturedResponder
            self.onAttach = onAttach
            self.shouldRestoreCapturedResponder = shouldRestoreCapturedResponder
            self.fallback = fallback
        }

        func attach(to window: NSWindow?) {
            guard let window, window !== hostWindow else { return }
            hostWindow = window
            // A keyboard-opened palette already had its focus cleared (and
            // the responder stashed) by the key monitor; a menu-opened one
            // still holds the responder to capture here.
            previousResponder = takeCapturedResponder() ?? window.firstResponder
            window.makeFirstResponder(nil)
            Task { @MainActor [weak self] in self?.onAttach() }
        }

        func restore() {
            guard let window = hostWindow else { return }
            if shouldRestoreCapturedResponder(),
               let view = previousResponder as? NSView,
               view.window === window,
               view.acceptsFirstResponder,
               window.makeFirstResponder(view) {
                return
            }
            fallback()
        }
    }
}

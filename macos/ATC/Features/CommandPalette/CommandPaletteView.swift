import AppKit
import SwiftUI

struct CommandPaletteView: View {
    @Environment(AppModel.self) private var appModel
    @Environment(WindowState.self) private var windowState
    @Environment(KeyboardConfigStore.self) private var configStore
    @Environment(WindowKeyboardRouter.self) private var router

    @State private var query = ""
    @State private var selectedID: CommandID?
    @State private var hoveredID: CommandID?
    @FocusState private var queryIsFocused: Bool

    var body: some View {
        let context = CommandContext(
            appModel: appModel,
            windowState: windowState,
            configStore: configStore
        )
        let rows = CommandPaletteContent.rows(
            query: query,
            keymap: configStore.keymap,
            context: context
        )

        ZStack(alignment: .top) {
            Color.clear
                .contentShape(Rectangle())
                .onTapGesture { dismissPalette() }

            palettePanel(rows: rows, context: context)
                .frame(maxWidth: 500)
                .padding(.horizontal, 20)
                .padding(.top, 48)
        }
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Command Palette")
        .accessibilityAddTraits(.isModal)
        .onExitCommand { dismissPalette() }
        .background(PaletteWindowAccessor(
            onAttach: { queryIsFocused = true },
            fallback: { windowState.requestTerminalFocus() }
        ))
        .onChange(of: query) {
            selectedID = query.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
                ? nil
                : rows.first?.id
        }
    }

    private func palettePanel(
        rows: [CommandPaletteRow],
        context: CommandContext
    ) -> some View {
        VStack(spacing: 0) {
            TextField("Execute a command…", text: $query)
                .textFieldStyle(.plain)
                .font(.title3)
                .padding(.horizontal, 14)
                .padding(.vertical, 12)
                .focused($queryIsFocused)
                .accessibilityLabel("Command query")
                .onSubmit { activateSelection(in: rows, context: context) }
                .onKeyPress(.downArrow) { moveSelection(1, through: rows) }
                .onKeyPress(.upArrow) { moveSelection(-1, through: rows) }
                .onKeyPress("n") { press in
                    guard press.modifiers == .control else { return .ignored }
                    return moveSelection(1, through: rows)
                }
                .onKeyPress("p") { press in
                    guard press.modifiers == .control else { return .ignored }
                    return moveSelection(-1, through: rows)
                }
                .onKeyPress(.escape) {
                    dismissPalette()
                    return .handled
                }

            Divider()

            resultList(rows: rows, context: context)
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
        rows: [CommandPaletteRow],
        context: CommandContext
    ) -> some View {
        if rows.isEmpty {
            Text("No matching commands")
                .font(.callout)
                .foregroundStyle(.secondary)
                .frame(maxWidth: .infinity, minHeight: 44)
                .accessibilityHidden(true)
        } else {
            ScrollViewReader { proxy in
                ScrollView {
                    LazyVStack(spacing: 2) {
                        ForEach(rows) { row in
                            commandRow(row, rows: rows, context: context)
                                .id(row.id)
                        }
                    }
                    .padding(5)
                }
                .frame(height: min(200, CGFloat(rows.count * 42 + 10)))
                .onChange(of: selectedID) {
                    if let selectedID {
                        proxy.scrollTo(selectedID, anchor: .center)
                    }
                }
            }
        }
    }

    private func commandRow(
        _ row: CommandPaletteRow,
        rows: [CommandPaletteRow],
        context: CommandContext
    ) -> some View {
        let isSelected = selectedID == row.id
        let isHovered = hoveredID == row.id
        return HStack(spacing: 10) {
            VStack(alignment: .leading, spacing: 2) {
                highlightedTitle(row)
                    .font(.callout)
                if case .unavailable(let reason) = row.availability {
                    Text(reason)
                        .font(.caption2)
                        .foregroundStyle(.tertiary)
                }
            }
            Spacer(minLength: 12)
            if let shortcut = row.shortcut {
                Text(shortcut.displayDescription)
                    .font(.caption.monospaced().weight(.medium))
                    .foregroundStyle(.secondary)
            }
        }
        .padding(.horizontal, 10)
        .padding(.vertical, 7)
        .foregroundStyle(row.availability.isAvailable ? .primary : .secondary)
        .opacity(row.availability.isAvailable ? 1 : 0.6)
        .background {
            RoundedRectangle(cornerRadius: 6)
                .fill(isSelected ? Color.accentColor.opacity(0.65) :
                    isHovered ? Color.primary.opacity(0.08) : .clear)
        }
        .contentShape(Rectangle())
        .onHover { hovering in hoveredID = hovering ? row.id : nil }
        .onTapGesture {
            selectedID = row.id
            activate(row, context: context)
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(accessibilityLabel(for: row))
        .accessibilityAddTraits(isSelected ? .isSelected : [])
        .accessibilityAction { activate(row, context: context) }
    }

    private func highlightedTitle(_ row: CommandPaletteRow) -> Text {
        var text = Text("")
        var cursor = row.title.startIndex
        for range in row.matchedRanges.sorted(by: { $0.lowerBound < $1.lowerBound }) {
            text = text + Text(String(row.title[cursor..<range.lowerBound]))
            text = text + Text(String(row.title[range])).bold()
            cursor = range.upperBound
        }
        return text + Text(String(row.title[cursor...]))
    }

    private func accessibilityLabel(for row: CommandPaletteRow) -> String {
        var parts = [row.title]
        if case .unavailable(let reason) = row.availability {
            parts.append("Unavailable — \(reason)")
        }
        if let shortcut = row.shortcut {
            parts.append(shortcut.spokenDescription)
        }
        return parts.joined(separator: ", ")
    }

    private func activateSelection(
        in rows: [CommandPaletteRow],
        context: CommandContext
    ) {
        guard let selectedID,
              let row = rows.first(where: { $0.id == selectedID })
        else {
            dismissPalette()
            return
        }
        activate(row, context: context)
    }

    private func activate(_ row: CommandPaletteRow, context: CommandContext) {
        guard row.availability.isAvailable else {
            if case .unavailable(let reason) = row.availability {
                router.showUnavailable(reason: reason)
            }
            return
        }
        dismissPalette()
        CommandRegistry.execute(row.id, context: context)
    }

    private func dismissPalette() {
        windowState.isCommandPalettePresented = false
    }

    private func moveSelection(
        _ offset: Int,
        through rows: [CommandPaletteRow]
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

private struct PaletteWindowAccessor: NSViewRepresentable {
    let onAttach: @MainActor () -> Void
    let fallback: @MainActor () -> Void

    func makeCoordinator() -> Coordinator {
        Coordinator(onAttach: onAttach, fallback: fallback)
    }

    func makeNSView(context: Context) -> HostView {
        let view = HostView()
        view.onWindowChange = { [weak coordinator = context.coordinator] window in
            coordinator?.attach(to: window)
        }
        return view
    }

    func updateNSView(_ nsView: HostView, context: Context) {
        context.coordinator.onAttach = onAttach
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
        var onAttach: @MainActor () -> Void
        var fallback: @MainActor () -> Void
        private(set) weak var hostWindow: NSWindow?
        private weak var previousResponder: NSResponder?

        init(
            onAttach: @escaping @MainActor () -> Void,
            fallback: @escaping @MainActor () -> Void
        ) {
            self.onAttach = onAttach
            self.fallback = fallback
        }

        func attach(to window: NSWindow?) {
            guard let window, window !== hostWindow else { return }
            hostWindow = window
            previousResponder = window.firstResponder
            window.makeFirstResponder(nil)
            Task { @MainActor [weak self] in self?.onAttach() }
        }

        func restore() {
            guard let window = hostWindow else { return }
            if let view = previousResponder as? NSView,
               view.window === window,
               view.acceptsFirstResponder,
               window.makeFirstResponder(view) {
                return
            }
            fallback()
        }
    }
}

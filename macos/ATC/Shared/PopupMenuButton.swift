// A caller-drawn button that pops a native NSMenu. SwiftUI's borderless
// `Menu` sizes to its label's compressed ideal width — every frame-based
// expansion collapses back to a text pill — so the label is an ordinary
// Button (layout the caller fully controls) and AppKit owns the popup:
// radio-style checkmarks, section headers, separators, and a destructive
// styling for entries that must never be a mis-click. `NavigatorDropdown`
// (the sidebar chip) and the Chat composer's settings controls both wrap it.

import AppKit
import SwiftUI

/// One entry in a `PopupMenuButton` menu.
enum PopupMenuEntry {
    case item(title: String, isSelected: Bool, isDestructive: Bool = false, action: @MainActor () -> Void)
    case header(String)
    case separator
}

/// How the label is drawn: `.plain` leaves it entirely to the caller (the
/// sidebar chip draws its own surface); `.accessoryBar` is the system's
/// quiet bar control — nothing at rest, a rollover highlight under the
/// pointer — for controls that must stay out of the way (the composer's).
enum PopupMenuAppearance {
    case plain
    case accessoryBar
}

struct PopupMenuButton<Label: View>: View {
    let entries: [PopupMenuEntry]
    var appearance: PopupMenuAppearance = .plain
    @ViewBuilder let label: () -> Label

    @State private var anchor = AnchorHolder()

    var body: some View {
        switch appearance {
        case .plain: button.buttonStyle(.plain)
        case .accessoryBar: button.buttonStyle(.accessoryBar)
        }
    }

    private var button: some View {
        Button {
            popUp()
        } label: {
            label()
        }
        .background {
            AnchorView(holder: anchor)
        }
    }

    private func popUp() {
        guard let view = anchor.view else { return }
        let menu = NSMenu()
        for entry in entries {
            switch entry {
            case .separator:
                menu.addItem(.separator())
            case .header(let title):
                menu.addItem(.sectionHeader(title: title))
            case .item(let title, let isSelected, let isDestructive, let action):
                let item = ClosureMenuItem(title: title, handler: action)
                item.state = isSelected ? .on : .off
                if isDestructive {
                    // attributedTitle replaces every attribute: keep the menu's
                    // own font, only the color changes.
                    item.attributedTitle = NSAttributedString(
                        string: title,
                        attributes: [
                            .foregroundColor: NSColor.systemRed,
                            .font: NSFont.menuFont(ofSize: 0),
                        ])
                    item.image = NSImage(
                        systemSymbolName: "exclamationmark.triangle.fill", accessibilityDescription: nil)
                }
                menu.addItem(item)
            }
        }
        menu.popUp(positioning: nil, at: NSPoint(x: 0, y: view.bounds.maxY + 4), in: view)
    }

    /// Bridges the SwiftUI position to AppKit: the invisible background
    /// view is the menu's positioning anchor.
    private final class AnchorHolder {
        weak var view: NSView?
    }

    private struct AnchorView: NSViewRepresentable {
        let holder: AnchorHolder

        func makeNSView(context: Context) -> NSView {
            let view = NSView()
            holder.view = view
            return view
        }

        func updateNSView(_ nsView: NSView, context: Context) {
            holder.view = nsView
        }
    }

    /// NSMenuItem needs a target/selector pair; this carries the closure
    /// and targets itself. Nonisolated to match NSMenuItem's initializers
    /// (Swift 6 rejects isolation-narrowing overrides); the handler stays
    /// main-actor because menu actions always fire there.
    private nonisolated final class ClosureMenuItem: NSMenuItem {
        private let handler: @MainActor () -> Void

        init(title: String, handler: @escaping @MainActor () -> Void) {
            self.handler = handler
            super.init(title: title, action: #selector(invoke), keyEquivalent: "")
            target = self
        }

        @available(*, unavailable)
        required init(coder: NSCoder) {
            fatalError("not used")
        }

        @objc @MainActor private func invoke() {
            handler()
        }
    }
}

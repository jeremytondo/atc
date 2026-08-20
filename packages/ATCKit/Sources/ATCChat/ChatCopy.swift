// Copy affordances shared by the Chat rows: one pasteboard seam for both
// platforms, a small copy button with checkmark feedback, and a hover
// overlay that reveals it on macOS (context menus carry the same action
// everywhere, so touch platforms lose nothing).

import ATCDesign
import SwiftUI

#if canImport(AppKit)
    import AppKit
#endif
#if canImport(UIKit)
    import UIKit
#endif

enum Clipboard {
    static func copy(_ string: String) {
        #if canImport(AppKit)
            NSPasteboard.general.clearContents()
            NSPasteboard.general.setString(string, forType: .string)
        #elseif canImport(UIKit)
            UIPasteboard.general.string = string
        #endif
    }
}

/// A quiet copy button that flashes a checkmark after copying.
struct CopyButton: View {
    let text: String

    @State private var copied = false
    /// Advances per tap so `.task(id:)` restarts the reset delay — an
    /// earlier tap's timer must not clear a later tap's checkmark.
    @State private var taps = 0

    var body: some View {
        Button {
            Clipboard.copy(text)
            withAnimation(.snappy(duration: 0.15)) { copied = true }
            taps += 1
        } label: {
            Image(systemName: copied ? "checkmark" : "document.on.document")
                .font(.caption)
                .contentTransition(.symbolEffect(.replace))
        }
        .buttonStyle(.plain)
        .foregroundStyle(copied ? Color.green : Color.secondary)
        .task(id: taps) {
            guard taps > 0 else { return }
            try? await Task.sleep(for: .seconds(1.2))
            guard !Task.isCancelled else { return }
            withAnimation(.snappy(duration: 0.3)) { copied = false }
        }
        .help("Copy")
        .accessibilityLabel("Copy")
    }
}

extension View {
    /// A hover-revealed copy button in the top-trailing corner, plus a
    /// Copy context-menu item — the affordance every message and detail
    /// block shares.
    func copyable(_ text: @autoclosure @escaping () -> String) -> some View {
        modifier(CopyableModifier(text: text))
    }
}

private struct CopyableModifier: ViewModifier {
    let text: () -> String

    @State private var hovering = false

    func body(content: Content) -> some View {
        content
            .onHover { hovering = $0 }
            .overlay(alignment: .topTrailing) {
                if hovering {
                    CopyButton(text: text())
                        .padding(Spacing.xs)
                        .background(.thinMaterial, in: RoundedRectangle(cornerRadius: Radius.control))
                        .padding(Spacing.xs)
                        .transition(.opacity)
                }
            }
            .contextMenu {
                Button("Copy") { Clipboard.copy(text()) }
            }
    }
}

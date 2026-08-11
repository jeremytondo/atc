// Keyboard selection movement for the search pickers (New Thread, Command
// Palette): focus stays in the query field, so the list is driven entirely by
// these four keys and the wrap-around rule they share.

import SwiftUI

enum SelectionMovement {
    /// Wrap-around movement over a row list: wraps at both ends, and a
    /// selection that is not in the current rows enters from the end the
    /// motion came from.
    static func wrapped<Row: Identifiable>(
        from current: Row.ID?,
        by offset: Int,
        in rows: [Row]
    ) -> Row.ID? {
        guard !rows.isEmpty else { return nil }
        guard let current, let index = rows.firstIndex(where: { $0.id == current }) else {
            return offset > 0 ? rows.first?.id : rows.last?.id
        }
        return rows[(index + offset + rows.count) % rows.count].id
    }
}

extension View {
    /// The picker movement bindings: ↓/↑ plus the Emacs-style Ctrl-N/Ctrl-P,
    /// all reported as an offset of ±1. Ctrl is matched exactly so ⌘N and
    /// friends fall through.
    func onSelectionMoveKeys(
        _ move: @escaping (Int) -> KeyPress.Result
    ) -> some View {
        onKeyPress(.downArrow) { move(1) }
            .onKeyPress(.upArrow) { move(-1) }
            .onKeyPress(keys: ["n"], phases: .down) { press in
                guard press.modifiers == .control else { return .ignored }
                return move(1)
            }
            .onKeyPress(keys: ["p"], phases: .down) { press in
                guard press.modifiers == .control else { return .ignored }
                return move(-1)
            }
    }
}

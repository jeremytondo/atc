import SwiftUI

/// The +/− bar under a settings master list.
struct ListEditorBar: View {
    var addHelp: String
    var removeHelp: String
    var canRemove: Bool
    var onAdd: () -> Void
    var onRemove: () -> Void

    var body: some View {
        HStack(spacing: 2) {
            Button(action: onAdd) {
                Image(systemName: "plus")
                    .frame(width: 24, height: 20)
            }
            .help(addHelp)
            Button(action: onRemove) {
                Image(systemName: "minus")
                    .frame(width: 24, height: 20)
            }
            .help(removeHelp)
            .disabled(!canRemove)
            Spacer()
        }
        .buttonStyle(.borderless)
        .padding(.horizontal, Spacing.sm)
        .padding(.vertical, Spacing.xs)
    }
}

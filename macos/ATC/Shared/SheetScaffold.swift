import SwiftUI

/// Standard chrome for form sheets: a Label title header, grouped Form
/// content by default (see `wrapsContentInForm`), and a trailing button row
/// in the HIG arrangement — Cancel adjacent-left of the primary action, both
/// trailing.
struct SheetScaffold<Content: View>: View {
    let title: String
    let systemImage: String
    /// The primary button's label; replaced by a spinner while `isBusy`.
    let primaryLabel: String
    var isBusy = false
    var canSubmit = true
    /// Grouped-Form content is the default; list-style sheets (the folder
    /// browser) pass false and lay out their own content edge-to-edge.
    var wrapsContentInForm = true
    /// Search-first launchers hide the title header — their search field is
    /// the visual anchor and the title would just push it down.
    var showsHeader = true
    var onCancel: () -> Void
    var onSubmit: () -> Void
    @ViewBuilder var content: () -> Content

    var body: some View {
        VStack(spacing: 0) {
            if showsHeader {
                Label(title, systemImage: systemImage)
                    .font(.headline)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(Spacing.md)
                Divider()
            }
            if wrapsContentInForm {
                Form(content: content)
                    .formStyle(.grouped)
            } else {
                content()
            }
            Divider()
            HStack {
                Spacer()
                Button("Cancel", role: .cancel, action: onCancel)
                    .keyboardShortcut(.cancelAction)
                Button(action: onSubmit) {
                    if isBusy {
                        ProgressView().controlSize(.small)
                    } else {
                        Text(primaryLabel)
                    }
                }
                .keyboardShortcut(.defaultAction)
                .disabled(!canSubmit)
            }
            .padding(Spacing.md)
        }
    }
}

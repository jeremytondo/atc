import SwiftUI

struct SheetPrimaryIndicator {
    let systemImage: String
    let color: Color
    let accessibilityLabel: String
}

/// Standard chrome for form sheets: a Label title header, grouped Form
/// content, and a trailing button row in the HIG arrangement — Cancel
/// adjacent-left of the primary action, both trailing.
struct SheetScaffold<Content: View>: View {
    let title: String
    let systemImage: String
    /// The primary button's label; replaced by a spinner while `isBusy`.
    let primaryLabel: String
    var isBusy = false
    var canSubmit = true
    var cancelDisabled = false
    var primaryIndicator: SheetPrimaryIndicator?
    var secondaryLabel: String?
    var onSecondary: (() -> Void)?
    var onCancel: () -> Void
    var onSubmit: () -> Void
    @ViewBuilder var content: () -> Content

    var body: some View {
        VStack(spacing: 0) {
            Label(title, systemImage: systemImage)
                .font(.headline)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(Spacing.md)
            Divider()
            Form(content: content)
                .formStyle(.grouped)
            Divider()
            HStack {
                Spacer()
                Button("Cancel", role: .cancel, action: onCancel)
                    .keyboardShortcut(.cancelAction)
                    .disabled(cancelDisabled)
                if let secondaryLabel, let onSecondary {
                    Button(secondaryLabel, action: onSecondary)
                }
                if let primaryIndicator {
                    Image(systemName: primaryIndicator.systemImage)
                        .foregroundStyle(primaryIndicator.color)
                        .help(primaryIndicator.accessibilityLabel)
                        .accessibilityLabel(primaryIndicator.accessibilityLabel)
                }
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

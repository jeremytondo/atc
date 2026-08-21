// The Chat composer's settings controls (ATC-205), shaped like the Codex
// desktop composer's: quiet text controls on the composer's bottom row, no
// bezel at rest, each popping a native menu. The model control — the
// agent's mark, the provider's model name, and the reasoning level — pops
// the catalog with the selected model's levels beneath it (a model without
// effort support lists none); Mode and Access are their own controls, with
// Full access in warning color and behind a confirmation, so it is never a
// mis-click. When the row is too narrow for all three, Mode and Access fold
// into one ⋯ menu.
// The controls render the subject they are given (an agent and its
// settings) and emit one patch per choice; the caller decides where the
// patch goes — Chat sends it to the server, which validates it, applies
// the per-model reasoning rule, and repaints through the thread's refetch;
// the New Thread draft applies it locally. Nothing is derived here beyond
// looking the selected model up in the agent's catalog.

import ATCAppServerAPI
import ATCDesign
import SwiftUI

/// What the composer's controls describe: the agent whose catalog the
/// model menu lists, and the settings the controls show — a thread's, or a
/// New Thread draft's before any thread exists (ATC-216).
struct ComposerSubject: Equatable {
    let agentId: AgentID
    let settings: ThreadSettings

    init(agentId: AgentID, settings: ThreadSettings) {
        self.agentId = agentId
        self.settings = settings
    }

    init(thread: ATCThread) {
        self.init(agentId: thread.agentId, settings: thread.settings)
    }
}

struct ChatSettingsControls: View {
    let subject: ComposerSubject
    /// The agent's catalog, nil until the store has read it.
    let models: [AgentModel]?
    /// Why the catalog is missing, when a read failed (nil while loading).
    let modelsError: String?
    let update: (ThreadSettingsPatch) -> Void
    /// Ask for the catalog again (a failed read).
    let reloadModels: () -> Void

    /// The Full access confirmation is up (the one Access choice that is
    /// not applied straight from the menu).
    @State private var confirmingFullAccess = false

    private var settings: ThreadSettings { subject.settings }

    private var selectedModel: AgentModel? {
        models?.first { $0.value == settings.model }
    }

    private var modelName: String { selectedModel?.displayName ?? settings.model }

    var body: some View {
        ViewThatFits(in: .horizontal) {
            HStack(spacing: 0) {
                modelControl
                modeControl
                accessControl
            }
            HStack(spacing: 0) {
                modelControl
                PopupMenuButton(entries: settingsEntries, appearance: .accessoryBar) {
                    controlLabel { Image(systemName: "ellipsis") }
                }
                .help("Mode and access")
                .accessibilityLabel("Chat settings")
            }
        }
        .confirmationDialog("Turn on Full access?", isPresented: $confirmingFullAccess) {
            Button("Turn On Full Access", role: .destructive) { update(.init(access: .fullAccess)) }
        } message: {
            Text(
                """
                From the next turn the agent runs commands and edits files without asking,                 and outside the sandbox. Every other level asks first or stays in the workspace.
                """)
        }
    }

    private var modelControl: some View {
        PopupMenuButton(entries: modelEntries, appearance: .accessoryBar) {
            controlLabel(chevron: true) {
                Image(systemName: subject.agentId.systemImage)
                Text(modelName).foregroundStyle(.primary)
                if let reasoning = settings.reasoning {
                    Text(reasoning.displayName)
                }
            }
        }
        .help("Model and reasoning")
        .accessibilityLabel(
            "Model: \(modelName)" + (settings.reasoning.map { ", reasoning \($0.displayName)" } ?? ""))
    }

    private var modeControl: some View {
        PopupMenuButton(entries: modeEntries, appearance: .accessoryBar) {
            controlLabel { Text(settings.mode.displayName) }
        }
        .help("Mode")
        .accessibilityLabel("Mode: \(settings.mode.displayName)")
    }

    private var accessControl: some View {
        PopupMenuButton(entries: accessEntries, appearance: .accessoryBar) {
            controlLabel(tint: settings.access == .fullAccess ? .orange : nil) {
                if settings.access == .fullAccess {
                    Image(systemName: "exclamationmark.shield")
                }
                Text(settings.access.displayName)
            }
        }
        .help("Access")
        .accessibilityLabel("Access: \(settings.access.displayName)")
    }

    /// One control's face: quiet secondary text (or a warning tint), a
    /// chevron only where the label reads as a value (the model).
    private func controlLabel(
        chevron: Bool = false, tint: Color? = nil, @ViewBuilder _ content: () -> some View
    ) -> some View {
        HStack(spacing: Spacing.xs) {
            content()
            if chevron {
                Image(systemName: "chevron.down")
                    .font(.caption2.weight(.semibold))
            }
        }
        .font(.callout)
        .foregroundStyle(tint.map(AnyShapeStyle.init) ?? AnyShapeStyle(.secondary))
        .lineLimit(1)
        .padding(.horizontal, Spacing.xs)
        .padding(.vertical, 2)
    }

    // Re-picking the checked entry of any menu is a no-op (nothing to send).
    var modelEntries: [PopupMenuEntry] {
        guard let models else {
            guard let modelsError else { return [.header("Loading models…")] }
            return [
                .header(modelsError),
                .item(title: "Retry", isSelected: false) { reloadModels() },
            ]
        }
        var entries: [PopupMenuEntry] = [.header("Model")]
        for model in models {
            entries.append(
                .item(title: model.displayName, isSelected: model.value == settings.model) {
                    if model.value != settings.model { update(.init(model: model.value)) }
                })
        }
        if let selectedModel, !selectedModel.supportedEffortLevels.isEmpty {
            entries.append(.separator)
            entries.append(.header("Reasoning"))
            for level in selectedModel.supportedEffortLevels {
                entries.append(
                    .item(title: level.displayName, isSelected: settings.reasoning == level) {
                        if settings.reasoning != level { update(.init(reasoning: level)) }
                    })
            }
        }
        return entries
    }

    var modeEntries: [PopupMenuEntry] {
        ThreadMode.allCases.map { mode in
            .item(title: mode.displayName, isSelected: settings.mode == mode) {
                if settings.mode != mode { update(.init(mode: mode)) }
            }
        }
    }

    /// Full access alone goes through the confirmation.
    var accessEntries: [PopupMenuEntry] {
        ThreadAccess.allCases.map { access in
            .item(
                title: access.displayName,
                isSelected: settings.access == access,
                isDestructive: access == .fullAccess
            ) {
                guard access != settings.access else { return }
                if access == .fullAccess {
                    confirmingFullAccess = true
                } else {
                    update(.init(access: access))
                }
            }
        }
    }

    /// The ⋯ menu when the row is too narrow: Mode and Access as sections.
    var settingsEntries: [PopupMenuEntry] {
        [.header("Mode")] + modeEntries + [.separator, .header("Access")] + accessEntries
    }
}
